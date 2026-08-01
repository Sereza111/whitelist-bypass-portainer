# v0.5.0-alpha.46

## Remote-safe DNS and WB control isolation

The alpha.45 field log proved that the WB carrier itself recovered correctly:
eight KCP shards moved several megabytes with almost empty queues. The apparent
internet outage came from Android Automatic DNS forwarding the mobile
operator's resolver through the tunnel. DNS actually exits from Creator/VPS,
where that resolver did not answer.

Alpha.46 changes the failure path without rolling back the working carrier:

- Android Automatic DNS now uses remote-safe public resolvers; Custom DNS is
  unchanged;
- matching WB peers negotiate reliable DNS and priority control, reporting
  `caps=0xd9`;
- CONNECT and DNS use reserved KCP lane zero while seven lanes remain for bulk;
- repeated TCP timeouts for one exact endpoint receive a bounded 2–15 second
  cooldown after two real failures, cleared immediately by a successful dial;
- no destination is rewritten and no WB cookie or token leaves Android.

The Android and server builds must be updated together to activate the new WB
capabilities. VK remains the project's own bootstrap/fallback and no external
VPN is required.
