package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	userStoreSchema     = 1
	userSessionCookie   = "wlb_user_session"
	userSessionLifetime = 12 * time.Hour
	userInviteLifetime  = 24 * time.Hour
	passwordIterations  = 180_000
	minimumUserPassword = 12
)

type userAccount struct {
	ID           string     `json:"id"`
	Username     string     `json:"username"`
	PasswordSalt string     `json:"passwordSalt"`
	PasswordHash string     `json:"passwordHash"`
	CreatedAt    time.Time  `json:"createdAt"`
	DisabledAt   *time.Time `json:"disabledAt,omitempty"`
}

type userInvitation struct {
	ID        string    `json:"id"`
	TokenHash string    `json:"tokenHash"`
	CreatedAt time.Time `json:"createdAt"`
	ExpiresAt time.Time `json:"expiresAt"`
}

type userStoreSnapshot struct {
	Schema  int              `json:"schema"`
	Users   []userAccount    `json:"users"`
	Invites []userInvitation `json:"invites"`
}

type userSession struct {
	UserID    string
	ExpiresAt time.Time
}

type userAccountStore struct {
	mu       sync.Mutex
	file     string
	users    map[string]userAccount
	invites  map[string]userInvitation
	sessions map[string]userSession
}

type publicUser struct {
	ID         string     `json:"id"`
	Username   string     `json:"username"`
	CreatedAt  time.Time  `json:"createdAt"`
	DisabledAt *time.Time `json:"disabledAt,omitempty"`
}

func newUserAccountStore(dataDir string) (*userAccountStore, error) {
	store := &userAccountStore{
		file: filepath.Join(dataDir, "users.json"), users: make(map[string]userAccount),
		invites: make(map[string]userInvitation), sessions: make(map[string]userSession),
	}
	if err := store.load(); err != nil {
		return nil, err
	}
	return store, nil
}

func (store *userAccountStore) load() error {
	body, err := os.ReadFile(store.file)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var snapshot userStoreSnapshot
	if err := json.Unmarshal(body, &snapshot); err != nil {
		return fmt.Errorf("decode user store: %w", err)
	}
	for _, user := range snapshot.Users {
		store.users[user.ID] = user
	}
	for _, invite := range snapshot.Invites {
		if invite.ExpiresAt.After(time.Now()) {
			store.invites[invite.TokenHash] = invite
		}
	}
	return nil
}

func (store *userAccountStore) saveLocked() error {
	users := make([]userAccount, 0, len(store.users))
	for _, user := range store.users {
		users = append(users, user)
	}
	sort.Slice(users, func(i, j int) bool { return users[i].CreatedAt.Before(users[j].CreatedAt) })
	invites := make([]userInvitation, 0, len(store.invites))
	for hash, invite := range store.invites {
		if invite.ExpiresAt.After(time.Now()) {
			invites = append(invites, invite)
		} else {
			delete(store.invites, hash)
		}
	}
	body, err := json.MarshalIndent(userStoreSnapshot{Schema: userStoreSchema, Users: users, Invites: invites}, "", "  ")
	if err != nil {
		return err
	}
	tmp := store.file + ".tmp"
	if err := os.WriteFile(tmp, body, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, store.file)
}

func (store *userAccountStore) createInvitation() (string, userInvitation, error) {
	token := randomSecret()
	hash := tokenDigest(token)
	now := time.Now().UTC()
	invite := userInvitation{ID: randomID("invite"), TokenHash: hash, CreatedAt: now, ExpiresAt: now.Add(userInviteLifetime)}
	store.mu.Lock()
	defer store.mu.Unlock()
	store.invites[hash] = invite
	if err := store.saveLocked(); err != nil {
		delete(store.invites, hash)
		return "", userInvitation{}, err
	}
	return token, invite, nil
}

func (store *userAccountStore) register(inviteToken, username, password string) (userAccount, error) {
	username = strings.TrimSpace(username)
	if !validUsername(username) {
		return userAccount{}, errors.New("username must contain 3-32 letters, digits, dot, dash or underscore")
	}
	if len([]byte(password)) < minimumUserPassword || len([]byte(password)) > 256 {
		return userAccount{}, fmt.Errorf("password must contain at least %d characters", minimumUserPassword)
	}
	hash := tokenDigest(strings.TrimSpace(inviteToken))
	store.mu.Lock()
	defer store.mu.Unlock()
	invite, ok := store.invites[hash]
	if !ok || !invite.ExpiresAt.After(time.Now()) {
		return userAccount{}, errors.New("invitation is invalid or expired")
	}
	for _, existing := range store.users {
		if strings.EqualFold(existing.Username, username) {
			return userAccount{}, errors.New("username is already in use")
		}
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return userAccount{}, err
	}
	derived := derivePassword([]byte(password), salt, passwordIterations, 32)
	now := time.Now().UTC()
	user := userAccount{
		ID: randomID("user"), Username: username,
		PasswordSalt: base64.RawURLEncoding.EncodeToString(salt),
		PasswordHash: base64.RawURLEncoding.EncodeToString(derived), CreatedAt: now,
	}
	store.users[user.ID] = user
	delete(store.invites, hash)
	if err := store.saveLocked(); err != nil {
		delete(store.users, user.ID)
		store.invites[hash] = invite
		return userAccount{}, err
	}
	return user, nil
}

func (store *userAccountStore) login(username, password string) (userAccount, bool) {
	if len(username) > 64 || len(password) < 1 || len(password) > 256 {
		derivePassword([]byte("invalid-password"), make([]byte, 16), passwordIterations, 32)
		return userAccount{}, false
	}
	store.mu.Lock()
	var user userAccount
	for _, candidate := range store.users {
		if strings.EqualFold(candidate.Username, strings.TrimSpace(username)) {
			user = candidate
			break
		}
	}
	store.mu.Unlock()
	if user.ID == "" || user.DisabledAt != nil {
		// Keep unknown-user timing close to a real password check.
		derivePassword([]byte(password), make([]byte, 16), passwordIterations, 32)
		return userAccount{}, false
	}
	salt, saltErr := base64.RawURLEncoding.DecodeString(user.PasswordSalt)
	want, hashErr := base64.RawURLEncoding.DecodeString(user.PasswordHash)
	if saltErr != nil || hashErr != nil || len(want) != 32 {
		return userAccount{}, false
	}
	got := derivePassword([]byte(password), salt, passwordIterations, len(want))
	return user, subtle.ConstantTimeCompare(got, want) == 1
}

func (store *userAccountStore) newSession(userID string) string {
	token := randomSecret()
	store.mu.Lock()
	store.sessions[tokenDigest(token)] = userSession{UserID: userID, ExpiresAt: time.Now().UTC().Add(userSessionLifetime)}
	store.mu.Unlock()
	return token
}

func (store *userAccountStore) authenticate(r *http.Request) (userAccount, bool) {
	cookie, err := r.Cookie(userSessionCookie)
	if err != nil || cookie.Value == "" {
		return userAccount{}, false
	}
	hash := tokenDigest(cookie.Value)
	store.mu.Lock()
	defer store.mu.Unlock()
	session, ok := store.sessions[hash]
	if !ok || !session.ExpiresAt.After(time.Now()) {
		delete(store.sessions, hash)
		return userAccount{}, false
	}
	user, ok := store.users[session.UserID]
	if !ok || user.DisabledAt != nil {
		delete(store.sessions, hash)
		return userAccount{}, false
	}
	return user, true
}

func (store *userAccountStore) logout(r *http.Request) {
	if cookie, err := r.Cookie(userSessionCookie); err == nil {
		store.mu.Lock()
		delete(store.sessions, tokenDigest(cookie.Value))
		store.mu.Unlock()
	}
}

func (store *userAccountStore) listUsers() []publicUser {
	store.mu.Lock()
	defer store.mu.Unlock()
	result := make([]publicUser, 0, len(store.users))
	for _, user := range store.users {
		result = append(result, publicUser{ID: user.ID, Username: user.Username, CreatedAt: user.CreatedAt, DisabledAt: user.DisabledAt})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].CreatedAt.Before(result[j].CreatedAt) })
	return result
}

func (store *userAccountStore) setDisabled(userID string, disabled bool) (publicUser, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	user, ok := store.users[userID]
	if !ok {
		return publicUser{}, os.ErrNotExist
	}
	if disabled {
		now := time.Now().UTC()
		user.DisabledAt = &now
	} else {
		user.DisabledAt = nil
	}
	store.users[userID] = user
	if disabled {
		for hash, session := range store.sessions {
			if session.UserID == userID {
				delete(store.sessions, hash)
			}
		}
	}
	if err := store.saveLocked(); err != nil {
		return publicUser{}, err
	}
	return publicUser{ID: user.ID, Username: user.Username, CreatedAt: user.CreatedAt, DisabledAt: user.DisabledAt}, nil
}

func setUserSessionCookie(w http.ResponseWriter, r *http.Request, token string) {
	https := r.TLS != nil || strings.EqualFold(strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Proto"), ",")[0]), "https")
	http.SetCookie(w, &http.Cookie{
		Name: userSessionCookie, Value: token, Path: "/", MaxAge: int(userSessionLifetime.Seconds()),
		HttpOnly: true, Secure: https, SameSite: http.SameSiteStrictMode,
	})
}

func clearUserSessionCookie(w http.ResponseWriter, r *http.Request) {
	https := r.TLS != nil || strings.EqualFold(strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Proto"), ",")[0]), "https")
	http.SetCookie(w, &http.Cookie{Name: userSessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: https, SameSite: http.SameSiteStrictMode})
}

func validUsername(value string) bool {
	if len(value) < 3 || len(value) > 32 {
		return false
	}
	for _, char := range value {
		if char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '.' || char == '-' || char == '_' {
			continue
		}
		return false
	}
	return true
}

func tokenDigest(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

// derivePassword implements PBKDF2-HMAC-SHA256 without adding a runtime
// dependency to the small Manager image.
func derivePassword(password, salt []byte, iterations, length int) []byte {
	result := make([]byte, 0, length)
	for block := uint32(1); len(result) < length; block++ {
		counter := []byte{byte(block >> 24), byte(block >> 16), byte(block >> 8), byte(block)}
		mac := hmac.New(sha256.New, password)
		_, _ = mac.Write(salt)
		_, _ = mac.Write(counter)
		u := mac.Sum(nil)
		t := append([]byte(nil), u...)
		for index := 1; index < iterations; index++ {
			mac = hmac.New(sha256.New, password)
			_, _ = mac.Write(u)
			u = mac.Sum(nil)
			for offset := range t {
				t[offset] ^= u[offset]
			}
		}
		result = append(result, t...)
	}
	return result[:length]
}
