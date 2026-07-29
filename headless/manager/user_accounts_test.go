package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestUserInvitationIsOneTimeAndPasswordIsHashed(t *testing.T) {
	store, err := newUserAccountStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := store.createInvitation()
	if err != nil {
		t.Fatal(err)
	}
	user, err := store.register(token, "alice", "correct-horse-battery")
	if err != nil {
		t.Fatal(err)
	}
	if user.PasswordHash == "correct-horse-battery" || user.PasswordSalt == "" {
		t.Fatal("password was not salted and hashed")
	}
	if _, err := store.register(token, "second", "correct-horse-battery"); err == nil {
		t.Fatal("one-time invitation was accepted twice")
	}
	if _, ok := store.login("ALICE", "correct-horse-battery"); !ok {
		t.Fatal("valid user login failed")
	}
	if _, ok := store.login("alice", "wrong-password-value"); ok {
		t.Fatal("invalid password was accepted")
	}
}

func TestUserPortalReturnsOnlyOwnedProfiles(t *testing.T) {
	dataDir := t.TempDir()
	cp, err := newControlPlane(dataDir, 4)
	if err != nil {
		t.Fatal(err)
	}
	users, err := newUserAccountStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	newUser := func(name string) userAccount {
		token, _, inviteErr := users.createInvitation()
		if inviteErr != nil {
			t.Fatal(inviteErr)
		}
		user, registerErr := users.register(token, name, "correct-horse-battery")
		if registerErr != nil {
			t.Fatal(registerErr)
		}
		return user
	}
	alice, bob := newUser("alice"), newUser("bob")
	enabled := true
	makeProfile := func(owner, name string) clientProfile {
		profile, profileErr := cp.createProfileFor(owner, profileInput{
			Name: name, Enabled: &enabled, MaxSessions: 1,
			Config: sessionRequest{Mode: "vk", Resources: "default", DisplayName: name},
		})
		if profileErr != nil {
			t.Fatal(profileErr)
		}
		return profile
	}
	aliceProfile := makeProfile(alice.ID, "Alice phone")
	bobProfile := makeProfile(bob.ID, "Bob phone")

	mux := http.NewServeMux()
	registerUserPortalRoutes(mux, cp, users, nil, "admin", testPanelPassword, t.TempDir())
	sessionToken := users.newSession(alice.ID)
	request := httptest.NewRequest(http.MethodGet, "/api/user/profiles", nil)
	request.AddCookie(&http.Cookie{Name: userSessionCookie, Value: sessionToken})
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), aliceProfile.ID) || strings.Contains(response.Body.String(), bobProfile.ID) {
		t.Fatalf("owned profile list status=%d body=%s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), alice.ID) || strings.Contains(response.Body.String(), aliceProfile.RecoveryKey) {
		t.Fatal("user profile response exposed owner or recovery secret")
	}

	request = httptest.NewRequest(http.MethodDelete, "/api/user/profiles/"+bobProfile.ID, nil)
	request.AddCookie(&http.Cookie{Name: userSessionCookie, Value: sessionToken})
	response = httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("cross-owner delete status=%d body=%s", response.Code, response.Body.String())
	}
	if _, ok := cp.profile(bobProfile.ID); !ok {
		t.Fatal("cross-owner delete removed the other user's profile")
	}
}
