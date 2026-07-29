package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const controlPlaneSchema = 5

type clientProfile struct {
	ID                 string         `json:"id"`
	OwnerID            string         `json:"ownerId,omitempty"`
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
	RecoveryRecipient  *string        `json:"recoveryRecipient,omitempty"`
	RecoveryVerifiedAt *time.Time     `json:"recoveryVerifiedAt,omitempty"`
	CurrentInvite      string         `json:"currentInvite,omitempty"`
	InviteGeneration   int            `json:"inviteGeneration,omitempty"`
	InviteUpdatedAt    *time.Time     `json:"inviteUpdatedAt,omitempty"`
}

func profileInviteReady(profile clientProfile) bool {
	return profile.CurrentInvite != "" && profile.InviteGeneration == profile.RecoveryGeneration
}

func profileBootstrapInvite(profile clientProfile) string {
	if profile.InviteGeneration > 0 && strings.EqualFold(profile.Config.Mode, "wbstream") &&
		validMobileInviteLink("wbstream", profile.CurrentInvite) {
		return profile.CurrentInvite
	}
	return ""
}

func managerLogContains(mgr *manager, needle string) bool {
	for _, line := range mgr.status().Logs {
		if strings.Contains(strings.ToLower(line), strings.ToLower(needle)) {
			return true
		}
	}
	return false
}

type panelSettings struct {
	RecoveryRecipient  string     `json:"recoveryRecipient,omitempty"`
	RecoveryVerifiedAt *time.Time `json:"recoveryVerifiedAt,omitempty"`
	UpdatedAt          *time.Time `json:"updatedAt,omitempty"`
}

type profileInput struct {
	Name              string         `json:"name"`
	Enabled           *bool          `json:"enabled,omitempty"`
	MaxSessions       int            `json:"maxSessions"`
	ExpiresAt         *time.Time     `json:"expiresAt,omitempty"`
	Config            sessionRequest `json:"config"`
	AutoRestart       *bool          `json:"autoRestart,omitempty"`
	RecoveryRecipient optionalString `json:"recoveryRecipient,omitempty"`
}

type optionalString struct {
	Present bool
	Value   *string
}

func (o *optionalString) UnmarshalJSON(data []byte) error {
	o.Present = true
	if string(data) == "null" {
		o.Value = nil
		return nil
	}
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	o.Value = &value
	return nil
}

type sessionInput struct {
	ClientID string          `json:"clientId"`
	Config   *sessionRequest `json:"config,omitempty"`
}

type managedSession struct {
	ID           string
	OwnerID      string
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
	Settings panelSettings   `json:"settings,omitempty"`
	Profiles []clientProfile `json:"profiles"`
}

type controlPlane struct {
	mu                sync.Mutex
	dataDir           string
	stateFile         string
	managedSecretsDir string
	maxSessions       int
	settings          panelSettings
	profiles          map[string]clientProfile
	sessions          map[string]*managedSession
	events            *eventLog
	wbCreator         *wbLoginManager
}

func (cp *controlPlane) setWBCreator(login *wbLoginManager) {
	cp.mu.Lock()
	cp.wbCreator = login
	cp.mu.Unlock()
	if login != nil {
		login.setEventLog(cp.events)
	}
}

func (cp *controlPlane) updateProfileInvite(profileID, link string) {
	cp.mu.Lock()
	defer cp.mu.Unlock()
	profile, ok := cp.profiles[profileID]
	if !ok {
		return
	}
	now := time.Now().UTC()
	profile.CurrentInvite = link
	profile.InviteGeneration = profile.RecoveryGeneration
	profile.InviteUpdatedAt = &now
	profile.UpdatedAt = now
	cp.profiles[profileID] = profile
	if err := cp.saveLocked(); err != nil {
		cp.events.add("error", "profile", "Could not persist refreshed WB invite", profileID)
		return
	}
	cp.events.add("info", "profile", "WB relay started; updated client profile with a fresh invite", profileID)
}

func newControlPlane(dataDir string, maxSessions int) (*controlPlane, error) {
	if maxSessions < 1 {
		maxSessions = 4
	}
	cp := &controlPlane{
		dataDir:           dataDir,
		stateFile:         filepath.Join(dataDir, "control-plane.json"),
		managedSecretsDir: filepath.Join(dataDir, "managed-secrets"),
		maxSessions:       maxSessions,
		settings:          panelSettings{},
		profiles:          make(map[string]clientProfile),
		sessions:          make(map[string]*managedSession),
		events:            newEventLog(200),
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
	if snapshot.Schema < 3 {
		migrated = true
	}
	cp.settings = snapshot.Settings
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
	body, err := json.MarshalIndent(controlPlaneSnapshot{Schema: controlPlaneSchema, Settings: cp.settings, Profiles: profiles}, "", "  ")
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
	return cp.listProfilesFor("", true)
}

func (cp *controlPlane) listProfilesFor(ownerID string, admin bool) []clientProfile {
	cp.mu.Lock()
	defer cp.mu.Unlock()
	result := make([]clientProfile, 0, len(cp.profiles))
	for _, profile := range cp.profiles {
		if !admin && profile.OwnerID != ownerID {
			continue
		}
		result = append(result, profile)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].CreatedAt.Before(result[j].CreatedAt) })
	return result
}

func (cp *controlPlane) profile(id string) (clientProfile, bool) {
	return cp.profileFor("", id, true)
}

func (cp *controlPlane) profileFor(ownerID, id string, admin bool) (clientProfile, bool) {
	cp.mu.Lock()
	defer cp.mu.Unlock()
	profile, ok := cp.profiles[id]
	if ok && !admin && profile.OwnerID != ownerID {
		return clientProfile{}, false
	}
	return profile, ok
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
		profile.OwnerID = previous.OwnerID
		profile.CreatedAt = previous.CreatedAt
		profile.AutoRestart = previous.AutoRestart
		profile.RecoveryKey = previous.RecoveryKey
		profile.RecoveryGeneration = previous.RecoveryGeneration
		profile.RecoveryRecipient = previous.RecoveryRecipient
		profile.RecoveryVerifiedAt = previous.RecoveryVerifiedAt
	}
	if input.RecoveryRecipient.Present {
		if input.RecoveryRecipient.Value == nil || strings.TrimSpace(*input.RecoveryRecipient.Value) == "" {
			profile.RecoveryRecipient = nil
			profile.RecoveryVerifiedAt = nil
		} else {
			normalized, err := normalizeVKRecipient(*input.RecoveryRecipient.Value)
			if err != nil {
				return clientProfile{}, err
			}
			profile.RecoveryRecipient = &normalized
			profile.RecoveryVerifiedAt = nil
		}
	}
	if input.AutoRestart != nil {
		profile.AutoRestart = *input.AutoRestart
	}
	profile.UpdatedAt = now
	return profile, nil
}

func (cp *controlPlane) createProfile(input profileInput) (clientProfile, error) {
	return cp.createProfileFor("", input)
}

func (cp *controlPlane) createProfileFor(ownerID string, input profileInput) (clientProfile, error) {
	cp.mu.Lock()
	defer cp.mu.Unlock()
	profile, err := cp.normalizeProfile(input, nil)
	if err != nil {
		return clientProfile{}, err
	}
	profile.OwnerID = ownerID
	cp.profiles[profile.ID] = profile
	if err := cp.saveLocked(); err != nil {
		delete(cp.profiles, profile.ID)
		return clientProfile{}, err
	}
	cp.events.add("info", "profile", fmt.Sprintf("Created client profile %q", profile.Name), profile.ID)
	return profile, nil
}

func (cp *controlPlane) updateProfile(id string, input profileInput) (clientProfile, error) {
	return cp.updateProfileFor("", id, input, true)
}

func (cp *controlPlane) updateProfileFor(ownerID, id string, input profileInput, admin bool) (clientProfile, error) {
	cp.mu.Lock()
	defer cp.mu.Unlock()
	previous, ok := cp.profiles[id]
	if !ok || (!admin && previous.OwnerID != ownerID) {
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
	cp.events.add("info", "profile", fmt.Sprintf("Updated client profile %q", profile.Name), profile.ID)
	return profile, nil
}

func (cp *controlPlane) duplicateProfile(id string) (clientProfile, error) {
	return cp.duplicateProfileFor("", id, true)
}

func (cp *controlPlane) duplicateProfileFor(ownerID, id string, admin bool) (clientProfile, error) {
	cp.mu.Lock()
	defer cp.mu.Unlock()
	source, ok := cp.profiles[id]
	if !ok || (!admin && source.OwnerID != ownerID) {
		return clientProfile{}, os.ErrNotExist
	}
	now := time.Now().UTC()
	copyName := strings.TrimSpace(source.Name) + " (copy)"
	if len([]rune(copyName)) > 80 {
		copyName = string([]rune(copyName)[:80])
	}
	profile := source
	profile.ID = randomID("client")
	profile.Name = copyName
	profile.RecoveryKey = randomSecret()
	profile.RecoveryGeneration = 0
	profile.RecoveryVerifiedAt = nil
	profile.CreatedAt = now
	profile.UpdatedAt = now
	cp.profiles[profile.ID] = profile
	if err := cp.saveLocked(); err != nil {
		delete(cp.profiles, profile.ID)
		return clientProfile{}, err
	}
	cp.events.add("info", "profile", fmt.Sprintf("Duplicated client profile %q", source.Name), profile.ID)
	return profile, nil
}

func (cp *controlPlane) deleteProfile(id string) error {
	return cp.deleteProfileFor("", id, true)
}

func (cp *controlPlane) deleteProfileFor(ownerID, id string, admin bool) error {
	cp.mu.Lock()
	defer cp.mu.Unlock()
	profile, ok := cp.profiles[id]
	if !ok || (!admin && profile.OwnerID != ownerID) {
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
	cp.events.add("warn", "profile", fmt.Sprintf("Deleted client profile %q", previous.Name), id)
	return nil
}

func (cp *controlPlane) startSession(input sessionInput) (sessionView, error) {
	return cp.startSessionFor("", input, true)
}

func profileWantsPersistentSession(profile clientProfile, now time.Time) bool {
	return profile.Enabled && profile.AutoRestart &&
		(profile.ExpiresAt == nil || !now.After(*profile.ExpiresAt))
}

func (cp *controlPlane) hasLiveSessionForProfile(profileID string) bool {
	cp.mu.Lock()
	defer cp.mu.Unlock()
	for _, session := range cp.sessions {
		if session.ClientID != profileID {
			continue
		}
		state := session.Manager.status().State
		if (state != "stopped" && state != "failed") || session.isRecovering() {
			return true
		}
	}
	return false
}

// restorePersistentProfiles reconciles the persisted desired state after a
// Manager/container restart. AutoRestart already supervises a process once it
// exists; this boot pass recreates the missing managedSession objects.
func (cp *controlPlane) restorePersistentProfiles() {
	now := time.Now()
	for _, profile := range cp.listProfiles() {
		if !profileWantsPersistentSession(profile, now) || cp.hasLiveSessionForProfile(profile.ID) {
			continue
		}
		if _, err := cp.startSession(sessionInput{ClientID: profile.ID}); err != nil {
			log.Printf("[manager] persistent profile restore skipped profile=%s mode=%s: %v", profile.ID, profile.Config.Mode, err)
			cp.events.add("warn", "session", "Could not restore the always-on profile after Manager restart", profile.ID)
			continue
		}
		log.Printf("[manager] restored persistent profile=%s mode=%s", profile.ID, profile.Config.Mode)
		cp.events.add("info", "session", "Restored always-on profile after Manager restart", profile.ID)
	}
}

func (cp *controlPlane) startSessionFor(ownerID string, input sessionInput, admin bool) (sessionView, error) {
	cp.mu.Lock()
	profile, ok := cp.profiles[input.ClientID]
	if !ok || (!admin && profile.OwnerID != ownerID) {
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
	bootstrapInvite := ""
	if strings.EqualFold(config.Mode, "wbstream") {
		bootstrapInvite = profileBootstrapInvite(profile)
	}
	if bootstrapInvite == "" {
		profile.RecoveryGeneration++
		cp.profiles[profile.ID] = profile
		if err := cp.saveLocked(); err != nil {
			cp.mu.Unlock()
			return sessionView{}, fmt.Errorf("persist recovery generation: %w", err)
		}
	} else {
		config.ExistingLink = bootstrapInvite
		config.DeviceInvite = true
	}
	config.RecoveryProfile = profile.ID
	config.RecoveryName = profile.Name
	config.RecoveryKey = profile.RecoveryKey
	config.RecoveryGeneration = profile.RecoveryGeneration
	id := randomID("session")
	sessionDir := filepath.Join(cp.dataDir, "sessions", id)
	mgr := newManagerAt(sessionDir)
	mgr.managedSecretsDir = cp.managedSecretsDir
	mgr.peerID = cp.effectiveRecoveryRecipientLocked(profile.ID)
	mgr.wbCreator = cp.wbCreator
	if config.Mode == "wbstream" {
		mgr.onLinkReady = func(link string) { cp.updateProfileInvite(profile.ID, link) }
	}
	created := time.Now().UTC()
	session := &managedSession{
		ID: id, OwnerID: profile.OwnerID, ClientID: input.ClientID, ClientName: profile.Name, CreatedAt: created,
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
	if bootstrapInvite != "" {
		cp.events.add("info", "wb-creator", "Reused the last validated WB room as the whitelist bootstrap", profile.ID)
	}
	if session.AutoRestart {
		go cp.superviseSession(session)
	}
	cp.events.add("info", "session", fmt.Sprintf("Started session for %q", profile.Name), id)
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
			session.StateMu.Lock()
			config := session.Config
			bootstrapInvite := profileBootstrapInvite(profile)
			if bootstrapInvite != "" && managerLogContains(session.Manager, "guests cannot create rooms") {
				// The old room is terminally invalid. Do not spend the recovery
				// budget retrying it; move the profile back to creator-requesting
				// state so Android can supply a fresh invitation.
				profile.CurrentInvite = ""
				profile.InviteGeneration = 0
				profile.InviteUpdatedAt = nil
				cp.profiles[profile.ID] = profile
				bootstrapInvite = ""
				cp.events.add("warn", "wb-creator", "Last WB bootstrap room was rejected; requesting a fresh Android invitation", profile.ID)
			}
			if bootstrapInvite == "" {
				profile.RecoveryGeneration++
				cp.profiles[profile.ID] = profile
				if err := cp.saveLocked(); err != nil {
					session.StateMu.Unlock()
					cp.mu.Unlock()
					continue
				}
				config.ExistingLink = ""
				config.DeviceInvite = false
			} else {
				config.ExistingLink = bootstrapInvite
				config.DeviceInvite = true
			}
			cp.mu.Unlock()
			session.Generation = profile.RecoveryGeneration
			config.RecoveryGeneration = session.Generation
			session.NextRetryAt = nil
			session.StateMu.Unlock()
			session.Manager.peerID, _ = cp.effectiveRecoveryRecipient(session.ClientID)
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
	return cp.listSessionsFor("", true)
}

func (cp *controlPlane) listSessionsFor(ownerID string, admin bool) []sessionView {
	cp.mu.Lock()
	sessions := make([]*managedSession, 0, len(cp.sessions))
	for _, session := range cp.sessions {
		if !admin && session.OwnerID != ownerID {
			continue
		}
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
	return cp.sessionFor("", id, true)
}

func (cp *controlPlane) sessionFor(ownerID, id string, admin bool) (sessionView, bool) {
	cp.mu.Lock()
	session, ok := cp.sessions[id]
	cp.mu.Unlock()
	if !ok || (!admin && session.OwnerID != ownerID) {
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
	return cp.stopSessionFor("", id, true)
}

func (cp *controlPlane) stopSessionFor(ownerID, id string, admin bool) (sessionView, error) {
	cp.mu.Lock()
	session, ok := cp.sessions[id]
	cp.mu.Unlock()
	if !ok || (!admin && session.OwnerID != ownerID) {
		return sessionView{}, os.ErrNotExist
	}
	session.StopOnce.Do(func() { close(session.StopCh) })
	session.StateMu.Lock()
	session.NextRetryAt = nil
	session.StateMu.Unlock()
	if err := session.Manager.stop(); err != nil {
		return sessionView{}, err
	}
	cp.events.add("info", "session", fmt.Sprintf("Stopped session for %q", session.ClientName), id)
	return cp.view(session), nil
}

func (cp *controlPlane) deleteSession(id string) error {
	return cp.deleteSessionFor("", id, true)
}

func (cp *controlPlane) deleteSessionFor(ownerID, id string, admin bool) error {
	cp.mu.Lock()
	session, ok := cp.sessions[id]
	if !ok || (!admin && session.OwnerID != ownerID) {
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
	cp.events.add("info", "session", "Removed stopped session", id)
	return os.RemoveAll(filepath.Join(cp.dataDir, "sessions", id))
}

func (cp *controlPlane) effectiveRecoveryRecipient(profileID string) (string, string) {
	cp.mu.Lock()
	defer cp.mu.Unlock()
	return cp.effectiveRecoverySourceLocked(profileID)
}

func (cp *controlPlane) effectiveRecoveryRecipientLocked(profileID string) string {
	id, _ := cp.effectiveRecoverySourceLocked(profileID)
	return id
}

func (cp *controlPlane) effectiveRecoverySourceLocked(profileID string) (string, string) {
	if profileID != "" {
		if profile, ok := cp.profiles[profileID]; ok && profile.RecoveryRecipient != nil {
			if value := strings.TrimSpace(*profile.RecoveryRecipient); value != "" {
				return value, "profile"
			}
		}
	}
	if value := strings.TrimSpace(cp.settings.RecoveryRecipient); value != "" {
		return value, "panel"
	}
	if value := strings.TrimSpace(os.Getenv("VK_PEER_ID")); value != "" {
		return value, "env"
	}
	return "", ""
}

func (cp *controlPlane) setGlobalRecoveryRecipient(raw string) error {
	normalized := ""
	if strings.TrimSpace(raw) != "" {
		var err error
		normalized, err = normalizeVKRecipient(raw)
		if err != nil {
			return err
		}
	}
	cp.mu.Lock()
	defer cp.mu.Unlock()
	cp.settings.RecoveryRecipient = normalized
	cp.settings.RecoveryVerifiedAt = nil
	now := time.Now().UTC()
	cp.settings.UpdatedAt = &now
	if err := cp.saveLocked(); err != nil {
		return err
	}
	cp.events.add("info", "recovery", "Updated global recovery recipient", "")
	return nil
}

func (cp *controlPlane) markGlobalRecoveryVerified() error {
	cp.mu.Lock()
	defer cp.mu.Unlock()
	now := time.Now().UTC()
	cp.settings.RecoveryVerifiedAt = &now
	cp.settings.UpdatedAt = &now
	return cp.saveLocked()
}

func (cp *controlPlane) markProfileRecoveryVerified(id string) error {
	cp.mu.Lock()
	defer cp.mu.Unlock()
	profile, ok := cp.profiles[id]
	if !ok {
		return os.ErrNotExist
	}
	now := time.Now().UTC()
	profile.RecoveryVerifiedAt = &now
	profile.UpdatedAt = now
	cp.profiles[id] = profile
	return cp.saveLocked()
}

func (cp *controlPlane) recoveryConfigured() bool {
	id, _ := cp.effectiveRecoveryRecipient("")
	return id != ""
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
