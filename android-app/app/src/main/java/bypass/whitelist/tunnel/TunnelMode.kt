package bypass.whitelist.tunnel

enum class TunnelMode(val label: String, val relayArg: String, val isPion: Boolean) {
    SMART("Smart · experimental", "smart", true),
    DC("DC · experimental", "dc", false),
    VIDEO("Video · recommended", "video", true);

    fun relayMode(platform: CallPlatform): String {
        if (!isPion) return "dc-joiner"
        return "${platform.id}-$relayArg-joiner"
    }

    fun forPlatform(platform: CallPlatform): TunnelMode {
        // Smart currently coordinates WB's reliable DataChannel and Video+KCP.
        // Other providers keep their existing Video path.
        if (this == SMART && platform != CallPlatform.WBSTREAM) {
            return VIDEO
        }
        if (this == DC && (platform == CallPlatform.TELEMOST || platform == CallPlatform.DION)) {
            return VIDEO
        }
        return this
    }
}
