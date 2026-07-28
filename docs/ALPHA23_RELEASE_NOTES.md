# Alpha.23 candidate: WB Android creator and profile sync

WB onboarding no longer uploads account state to Manager. The QR establishes a
persistent Android creator binding. Android signs in locally, creates a call in
the regular WB interface and submits only the finished invitation link.

Manager validates that link, starts the server relay as a guest participant and
stores the current invitation generation on the client profile. If the call
ends, the relay exits and Manager asks Android for a new call instead of trying
to revive the dead room.

Android mobile invites now carry a protected HTTPS profile-sync endpoint. While
the foreground Joiner is active it polls only the profile generation and call
link. A newer generation updates the saved destination and restarts the carrier
without replacing the VPN configuration.

The published `v0.5.0-alpha.22` APK predates this architecture. Branch artifacts
for this candidate are marked `0.5.0-alpha.23+<commit>` so they cannot be
mistaken for the old release asset.
