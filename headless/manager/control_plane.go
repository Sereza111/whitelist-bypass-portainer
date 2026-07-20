package main

import (
	"crypto/rand"
	"encoding/base64"
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

const controlPlaneSchema = 2

type clientProfile struct {
	ID                 string         `json:"id"`
	Name               string         `json:"name"`
	Enabled            bool           `json:"enabled"`
	MaxSessions        int            `json:"maxSessions"`
	ExpiresAt          *time.Time     `json:"expiresAt,omitempty"`
	Config             sessionRequest `json:"config"`
	CreatedAt          time.Time      `json:"createdAt"`
	UpdatedAt          time.Time      `json:"updatedAt"`
	AutoRestart        bool           `json:"autoRestart"`
	RecoveryKey        string         `json:"recoveryKey"`
	RecoveryGeneration int            `json:"recoveryGeneration"`
}

type profileInput struct {
	Name        string         `json:"name"`
	Enabled     *bool          `json:"enabled,omitempty"`
	MaxSessions int            `json:"maxSessions"`
	ExpiresAt   *time.Time     `json:"expiresAt,omitempty"`
	Config      sessionRequest `json:"config"`
	AutoRestart *bool          `json:"autoRestart,omitempty"`
}

type sessionInput struct {
	ClientID string          `json:"clientId"`
	Config   *sessionRequest `json:"config,omitempty"`
}

type managedSession struct {
	ID           string
	ClientID     string
	ClientName   string
	CreatedAt    time.Time
	Manager      *manager
	Config       sessionRequest
	AutoRestart  bool
	StopCh       chan struct{}
	StopOnce     sync.Once
	StateMu      sync.Mutex
	Generation   int
	RestartCount int
	NextRetryAt  *time.Time
}

func (session *managedSession) isRecovering() bool {
	session.StateMu.Lock()
	defer session.StateMu.Unlock()
	return session.NextRetryAt != nil
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
	migrated := snapshot.Schema < controlPlaneSchema
	for _, profile := range snapshot.Profiles {
		if profile.RecoveryKey == "" {
			profile.RecoveryKey = randomSecret()
			profile.AutoRestart = true
			migrated = true
		}
		cp.profiles[profile.ID] = profile
	}
	if migrated {
		return cp.saveLocked()
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
		AutoRestart: true,
		RecoveryKey: randomSecret(),
	}
	if previous != nil {
		profile.ID = previous.ID
		profile.CreatedAt = previous.CreatedAt
		profile.AutoRestart = previous.AutoRestart
		profile.RecoveryKey = previous.RecoveryKey
		profile.RecoveryGeneration = previous.RecoveryGeneration
	}
	if input.AutoRestart != nil {
		profile.AutoRestart = *input.AutoRestart
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
		state := session.Manager.status().State
		if session.ClientID == id && ((state != "stopped" && state != "failed") || session.isRecovering()) {
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
		if (state != "stopped" && state != "failed") || session.isRecovering() {
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
	profile.RecoveryGeneration++
	cp.profiles[profile.ID] = profile
	if err := cp.saveLocked(); err != nil {
		cp.mu.Unlock()
		return sessionView{}, fmt.Errorf("persist recovery generation: %w", err)
	}
	config.RecoveryProfile = profile.ID
	config.RecoveryName = profile.Name
	config.RecoveryKey = profile.RecoveryKey
	config.RecoveryGeneration = profile.RecoveryGeneration
	id := randomID("session")
	sessionDir := filepath.Join(cp.dataDir, "sessions", id)
	mgr := newManagerAt(sessionDir)
	created := time.Now().UTC()
	session := &managedSession{
		ID: id, ClientID: input.ClientID, ClientName: profile.Name, CreatedAt: created,
		Manager: mgr, Config: config, AutoRestart: profile.AutoRestart,
		StopCh: make(chan struct{}), Generation: profile.RecoveryGeneration,
	}
	cp.sessions[id] = session
	cp.mu.Unlock()
	if err := mgr.start(config); err != nil {
		cp.mu.Lock()
		delete(cp.sessions, id)
		cp.mu.Unlock()
		return sessionView{}, err
	}
	if session.AutoRestart {
		go cp.superviseSession(session)
	}
	return cp.view(session), nil
}

func (cp *controlPlane) superviseSession(session *managedSession) {
	for {
		done := session.Manager.doneChannel()
		if done == nil {
			return
		}
		cycleStarted := time.Now()
		select {
		case <-done:
		case <-session.StopCh:
			return
		}
		if time.Since(cycleStarted) >= 2*time.Minute {
			session.StateMu.Lock()
			session.RestartCount = 0
			session.StateMu.Unlock()
		}
		for {
			session.StateMu.Lock()
			session.RestartCount++
			delay := recoveryDelay(session.RestartCount)
			next := time.Now().UTC().Add(delay)
			session.NextRetryAt = &next
			session.StateMu.Unlock()
			timer := time.NewTimer(delay)
			select {
			case <-timer.C:
			case <-session.StopCh:
				timer.Stop()
				return
			}
			cp.mu.Lock()
			profile, exists := cp.profiles[session.ClientID]
			_, retained := cp.sessions[session.ID]
			if !exists || !retained || !profile.Enabled || !profile.AutoRestart ||
				(profile.ExpiresAt != nil && time.Now().After(*profile.ExpiresAt)) {
				cp.mu.Unlock()
				return
			}
			profile.RecoveryGeneration++
			cp.profiles[profile.ID] = profile
			if err := cp.saveLocked(); err != nil {
				cp.mu.Unlock()
				continue
			}
			cp.mu.Unlock()
			session.StateMu.Lock()
			session.Generation = profile.RecoveryGeneration
			config := session.Config
			config.ExistingLink = ""
			config.RecoveryGeneration = session.Generation
			session.NextRetryAt = nil
			session.StateMu.Unlock()
			if err := session.Manager.start(config); err != nil {
				continue
			}
			break
		}
	}
}

func recoveryDelay(attempt int) time.Duration {
	delays := []time.Duration{2 * time.Second, 5 * time.Second, 10 * time.Second, 30 * time.Second, time.Minute, 2 * time.Minute, 5 * time.Minute}
	if attempt < 1 {
		return delays[0]
	}
	if attempt > len(delays) {
		return delays[len(delays)-1]
	}
	return delays[attempt-1]
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
	status := session.Manager.status()
	session.StateMu.Lock()
	status.Generation = session.Generation
	status.RestartCount = session.RestartCount
	status.NextRetryAt = session.NextRetryAt
	if session.NextRetryAt != nil && status.State != "stopping" {
		status.State = "recovering"
	}
	session.StateMu.Unlock()
	return sessionView{
		ID: session.ID, ClientID: session.ClientID, ClientName: session.ClientName,
		CreatedAt: session.CreatedAt, Status: status,
	}
}

func (cp *controlPlane) stopSession(id string) (sessionView, error) {
	cp.mu.Lock()
	session, ok := cp.sessions[id]
	cp.mu.Unlock()
	if !ok {
		return sessionView{}, os.ErrNotExist
	}
	session.StopOnce.Do(func() { close(session.StopCh) })
	session.StateMu.Lock()
	session.NextRetryAt = nil
	session.StateMu.Unlock()
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
	session.StopOnce.Do(func() { close(session.StopCh) })
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
		session.StopOnce.Do(func() { close(session.StopCh) })
		session.StateMu.Lock()
		session.NextRetryAt = nil
		session.StateMu.Unlock()
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

func randomSecret() string {
	var value [32]byte
	if _, err := rand.Read(value[:]); err != nil {
		return randomID("recovery")
	}
	return base64.RawURLEncoding.EncodeToString(value[:])
}
