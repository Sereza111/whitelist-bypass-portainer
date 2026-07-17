package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const controlPlaneSchema = 1

type clientProfile struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	Enabled     bool           `json:"enabled"`
	MaxSessions int            `json:"maxSessions"`
	ExpiresAt   *time.Time     `json:"expiresAt,omitempty"`
	Config      sessionRequest `json:"config"`
	CreatedAt   time.Time      `json:"createdAt"`
	UpdatedAt   time.Time      `json:"updatedAt"`
}

type profileInput struct {
	Name        string         `json:"name"`
	Enabled     *bool          `json:"enabled,omitempty"`
	MaxSessions int            `json:"maxSessions"`
	ExpiresAt   *time.Time     `json:"expiresAt,omitempty"`
	Config      sessionRequest `json:"config"`
}

type sessionInput struct {
	ClientID string          `json:"clientId"`
	Config   *sessionRequest `json:"config,omitempty"`
}

type managedSession struct {
	ID         string
	ClientID   string
	ClientName string
	CreatedAt  time.Time
	Manager    *manager
}

type sessionView struct {
	ID         string        `json:"id"`
	ClientID   string        `json:"clientId"`
	ClientName string        `json:"clientName"`
	CreatedAt  time.Time     `json:"createdAt"`
	Status     sessionStatus `json:"status"`
}

type controlPlaneSnapshot struct {
	Schema   int             `json:"schema"`
	Profiles []clientProfile `json:"profiles"`
}

type controlPlane struct {
	mu          sync.Mutex
	dataDir     string
	stateFile   string
	maxSessions int
	profiles    map[string]clientProfile
	sessions    map[string]*managedSession
}

func newControlPlane(dataDir string, maxSessions int) (*controlPlane, error) {
	if maxSessions < 1 {
		maxSessions = 4
	}
	cp := &controlPlane{
		dataDir:     dataDir,
		stateFile:   filepath.Join(dataDir, "control-plane.json"),
		maxSessions: maxSessions,
		profiles:    make(map[string]clientProfile),
		sessions:    make(map[string]*managedSession),
	}
	if err := os.MkdirAll(filepath.Join(dataDir, "sessions"), 0o700); err != nil {
		return nil, err
	}
	if err := cp.load(); err != nil {
		return nil, err
	}
	return cp, nil
}

func (cp *controlPlane) load() error {
	body, err := os.ReadFile(cp.stateFile)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var snapshot controlPlaneSnapshot
	if err := json.Unmarshal(body, &snapshot); err != nil {
		return fmt.Errorf("decode control-plane state: %w", err)
	}
	for _, profile := range snapshot.Profiles {
		cp.profiles[profile.ID] = profile
	}
	return nil
}

func (cp *controlPlane) saveLocked() error {
	profiles := make([]clientProfile, 0, len(cp.profiles))
	for _, profile := range cp.profiles {
		profiles = append(profiles, profile)
	}
	sort.Slice(profiles, func(i, j int) bool { return profiles[i].CreatedAt.Before(profiles[j].CreatedAt) })
	body, err := json.MarshalIndent(controlPlaneSnapshot{Schema: controlPlaneSchema, Profiles: profiles}, "", "  ")
	if err != nil {
		return err
	}
	tmp := cp.stateFile + ".tmp"
	if err := os.WriteFile(tmp, body, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, cp.stateFile)
}

func (cp *controlPlane) listProfiles() []clientProfile {
	cp.mu.Lock()
	defer cp.mu.Unlock()
	result := make([]clientProfile, 0, len(cp.profiles))
	for _, profile := range cp.profiles {
		result = append(result, profile)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].CreatedAt.Before(result[j].CreatedAt) })
	return result
}

func (cp *controlPlane) normalizeProfile(input profileInput, previous *clientProfile) (clientProfile, error) {
	name := strings.TrimSpace(input.Name)
	if name == "" && previous != nil {
		name = previous.Name
	}
	if name == "" || len([]rune(name)) > 80 {
		return clientProfile{}, errors.New("client name must contain 1-80 characters")
	}
	config, err := newManagerAt(cp.dataDir).normalizeRequest(input.Config)
	if err != nil {
		return clientProfile{}, err
	}
	// Existing call links are one-shot session secrets. Never persist them in a
	// reusable client profile or the control-plane state file.
	config.ExistingLink = ""
	maxSessions := input.MaxSessions
	if maxSessions == 0 && previous != nil {
		maxSessions = previous.MaxSessions
	}
	if maxSessions == 0 {
		maxSessions = 1
	}
	if maxSessions < 1 || maxSessions > cp.maxSessions {
		return clientProfile{}, fmt.Errorf("maxSessions must be between 1 and %d", cp.maxSessions)
	}
	enabled := true
	if previous != nil {
		enabled = previous.Enabled
	}
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	now := time.Now().UTC()
	profile := clientProfile{
		ID:          randomID("client"),
		Name:        name,
		Enabled:     enabled,
		MaxSessions: maxSessions,
		ExpiresAt:   input.ExpiresAt,
		Config:      config,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if previous != nil {
		profile.ID = previous.ID
		profile.CreatedAt = previous.CreatedAt
	}
	return profile, nil
}

func (cp *controlPlane) createProfile(input profileInput) (clientProfile, error) {
	cp.mu.Lock()
	defer cp.mu.Unlock()
	profile, err := cp.normalizeProfile(input, nil)
	if err != nil {
		return clientProfile{}, err
	}
	cp.profiles[profile.ID] = profile
	if err := cp.saveLocked(); err != nil {
		delete(cp.profiles, profile.ID)
		return clientProfile{}, err
	}
	return profile, nil
}

func (cp *controlPlane) updateProfile(id string, input profileInput) (clientProfile, error) {
	cp.mu.Lock()
	defer cp.mu.Unlock()
	previous, ok := cp.profiles[id]
	if !ok {
		return clientProfile{}, os.ErrNotExist
	}
	profile, err := cp.normalizeProfile(input, &previous)
	if err != nil {
		return clientProfile{}, err
	}
	cp.profiles[id] = profile
	if err := cp.saveLocked(); err != nil {
		cp.profiles[id] = previous
		return clientProfile{}, err
	}
	return profile, nil
}

func (cp *controlPlane) deleteProfile(id string) error {
	cp.mu.Lock()
	defer cp.mu.Unlock()
	if _, ok := cp.profiles[id]; !ok {
		return os.ErrNotExist
	}
	for _, session := range cp.sessions {
		if session.ClientID == id && session.Manager.status().State != "stopped" && session.Manager.status().State != "failed" {
			return errors.New("stop this client's active sessions before deleting it")
		}
	}
	previous := cp.profiles[id]
	delete(cp.profiles, id)
	if err := cp.saveLocked(); err != nil {
		cp.profiles[id] = previous
		return err
	}
	return nil
}

func (cp *controlPlane) startSession(input sessionInput) (sessionView, error) {
	cp.mu.Lock()
	profile, ok := cp.profiles[input.ClientID]
	if !ok {
		cp.mu.Unlock()
		return sessionView{}, fmt.Errorf("client profile not found")
	}
	if !profile.Enabled {
		cp.mu.Unlock()
		return sessionView{}, errors.New("client profile is disabled")
	}
	if profile.ExpiresAt != nil && time.Now().After(*profile.ExpiresAt) {
		cp.mu.Unlock()
		return sessionView{}, errors.New("client profile has expired")
	}
	activeTotal, activeClient := 0, 0
	for _, session := range cp.sessions {
		state := session.Manager.status().State
		if state != "stopped" && state != "failed" {
			activeTotal++
			if session.ClientID == input.ClientID {
				activeClient++
			}
		}
	}
	if activeTotal >= cp.maxSessions {
		cp.mu.Unlock()
		return sessionView{}, fmt.Errorf("server session limit reached (%d)", cp.maxSessions)
	}
	if activeClient >= profile.MaxSessions {
		cp.mu.Unlock()
		return sessionView{}, fmt.Errorf("client session limit reached (%d)", profile.MaxSessions)
	}
	config := profile.Config
	if input.Config != nil {
		config = *input.Config
	}
	id := randomID("session")
	sessionDir := filepath.Join(cp.dataDir, "sessions", id)
	mgr := newManagerAt(sessionDir)
	created := time.Now().UTC()
	session := &managedSession{ID: id, ClientID: input.ClientID, ClientName: profile.Name, CreatedAt: created, Manager: mgr}
	cp.sessions[id] = session
	cp.mu.Unlock()
	if err := mgr.start(config); err != nil {
		cp.mu.Lock()
		delete(cp.sessions, id)
		cp.mu.Unlock()
		return sessionView{}, err
	}
	return cp.view(session), nil
}

func (cp *controlPlane) listSessions() []sessionView {
	cp.mu.Lock()
	sessions := make([]*managedSession, 0, len(cp.sessions))
	for _, session := range cp.sessions {
		sessions = append(sessions, session)
	}
	cp.mu.Unlock()
	sort.Slice(sessions, func(i, j int) bool { return sessions[i].CreatedAt.After(sessions[j].CreatedAt) })
	result := make([]sessionView, 0, len(sessions))
	for _, session := range sessions {
		result = append(result, cp.view(session))
	}
	return result
}

func (cp *controlPlane) session(id string) (sessionView, bool) {
	cp.mu.Lock()
	session, ok := cp.sessions[id]
	cp.mu.Unlock()
	if !ok {
		return sessionView{}, false
	}
	return cp.view(session), true
}

func (cp *controlPlane) view(session *managedSession) sessionView {
	return sessionView{
		ID: session.ID, ClientID: session.ClientID, ClientName: session.ClientName,
		CreatedAt: session.CreatedAt, Status: session.Manager.status(),
	}
}

func (cp *controlPlane) stopSession(id string) (sessionView, error) {
	cp.mu.Lock()
	session, ok := cp.sessions[id]
	cp.mu.Unlock()
	if !ok {
		return sessionView{}, os.ErrNotExist
	}
	if err := session.Manager.stop(); err != nil {
		return sessionView{}, err
	}
	return cp.view(session), nil
}

func (cp *controlPlane) deleteSession(id string) error {
	cp.mu.Lock()
	session, ok := cp.sessions[id]
	if !ok {
		cp.mu.Unlock()
		return os.ErrNotExist
	}
	state := session.Manager.status().State
	if state != "stopped" && state != "failed" {
		cp.mu.Unlock()
		return errors.New("stop the session before removing it")
	}
	delete(cp.sessions, id)
	cp.mu.Unlock()
	return os.RemoveAll(filepath.Join(cp.dataDir, "sessions", id))
}

func (cp *controlPlane) stopAll() {
	cp.mu.Lock()
	sessions := make([]*managedSession, 0, len(cp.sessions))
	for _, session := range cp.sessions {
		sessions = append(sessions, session)
	}
	cp.mu.Unlock()
	for _, session := range sessions {
		_ = session.Manager.stop()
	}
}

func randomID(prefix string) string {
	var value [8]byte
	if _, err := rand.Read(value[:]); err != nil {
		return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
	}
	return prefix + "-" + hex.EncodeToString(value[:])
}
