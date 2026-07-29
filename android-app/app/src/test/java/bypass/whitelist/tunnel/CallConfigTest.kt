package bypass.whitelist.tunnel

import org.junit.Assert.assertEquals
import org.junit.Test

class CallConfigTest {

    @Test
    fun managedWbRefreshPreservesExplicitDcMode() {
        val existing = CallConfig.newWith("WB", "wbstream://old-room").copy(
            tunnelMode = TunnelMode.DC,
			tunnelModeExplicit = true,
            dualTrack = false,
            recoveryProfile = "client-profile",
        )

        val refreshed = CallConfig.managedInvite(
            existing = existing,
            name = "WB",
            url = "wbstream://new-room",
            profile = "client-profile",
            key = "abcdefghijklmnopqrstuvwxyz123456",
            generation = 2,
            syncUrl = "https://panel.example.test/api/client-profiles/client-profile/invite",
        )

        assertEquals(TunnelMode.DC, refreshed.tunnelMode)
        assertEquals(false, refreshed.dualTrack)
        assertEquals("wbstream://new-room", refreshed.url)
        assertEquals(2, refreshed.recoveryGeneration)
    }

	@Test
	fun managedWbRefreshPreservesExplicitVideoMode() {
		val existing = CallConfig.newWith("WB", "wbstream://old-room").copy(
			tunnelMode = TunnelMode.VIDEO,
			tunnelModeExplicit = true,
			recoveryProfile = "client-profile",
		)

		val refreshed = CallConfig.managedInvite(
			existing = existing,
			name = "WB",
			url = "wbstream://new-room",
			profile = "client-profile",
			key = "abcdefghijklmnopqrstuvwxyz123456",
			generation = 2,
			syncUrl = null,
		)

		assertEquals(TunnelMode.VIDEO, refreshed.tunnelMode)
		assertEquals(true, refreshed.tunnelModeExplicit)
	}

	@Test
	fun automaticManagedWbSmartMigratesToVideo() {
		val legacy = CallConfig.newWith("WB", "wbstream://old-room").copy(
			tunnelMode = TunnelMode.SMART,
			tunnelModeExplicit = false,
			recoveryProfile = "client-profile",
		)

		val refreshed = CallConfig.managedInvite(
			existing = legacy,
			name = "WB",
			url = "wbstream://new-room",
			profile = "client-profile",
			key = "abcdefghijklmnopqrstuvwxyz123456",
			generation = 2,
			syncUrl = null,
		)

		assertEquals(TunnelMode.VIDEO, refreshed.tunnelMode)
		assertEquals(TunnelMode.VIDEO, legacy.migrateManagedWbTransportDefault().tunnelMode)
	}

	@Test
	fun startupMigrationDoesNotOverrideExplicitOrManualVideo() {
		val explicit = CallConfig.newWith("WB", "wbstream://room-id").copy(
			tunnelMode = TunnelMode.VIDEO,
			tunnelModeExplicit = true,
			recoveryProfile = "client-profile",
		)
		val manual = CallConfig.newWith("WB", "wbstream://room-id").copy(
			tunnelMode = TunnelMode.VIDEO,
			tunnelModeExplicit = false,
			recoveryProfile = null,
		)

		assertEquals(TunnelMode.VIDEO, explicit.migrateManagedWbTransportDefault().tunnelMode)
		assertEquals(TunnelMode.VIDEO, manual.migrateManagedWbTransportDefault().tunnelMode)
	}

    @Test
	fun firstManagedWbInviteDefaultsToVideoDualTrack() {
        val created = CallConfig.managedInvite(
            existing = null,
            name = "WB",
            url = "wbstream://new-room",
            profile = "client-profile",
            key = "abcdefghijklmnopqrstuvwxyz123456",
            generation = 1,
            syncUrl = null,
        )

		assertEquals(TunnelMode.VIDEO, created.tunnelMode)
        assertEquals(true, created.dualTrack)
    }

	@Test
	fun newWbDestinationDefaultsToVideoWithoutChangingOtherProviders() {
		val wb = CallConfig.newWith("WB", "wbstream://room-id")
		val vk = CallConfig.newWith("VK", "https://vk.com/call/example")

		assertEquals(TunnelMode.VIDEO, wb.tunnelMode)
		assertEquals(true, wb.dualTrack)
		assertEquals(null, vk.tunnelMode)
		assertEquals(null, vk.dualTrack)
	}

	@Test
	fun wbControlBootstrapPrefersActiveThenManagedVkInvite() {
		val manual = CallConfig.newWith("Manual VK", "https://vk.com/call/manual-room")
		val managed = CallConfig.newWith("Managed VK", "https://vk.com/call/managed-room").copy(
			recoveryProfile = "client-vk-profile",
		)
		val wb = CallConfig.newWith("WB", "wbstream://wb-room")
		val invalid = CallConfig.newWith("Invalid", "https://attacker.example/call/not-vk")
		val items = listOf(manual, managed, wb, invalid)

		assertEquals(manual.id, CallConfig.selectVKControlBootstrap(items, manual.id)?.id)
		assertEquals(managed.id, CallConfig.selectVKControlBootstrap(items, "missing")?.id)
		assertEquals(null, CallConfig.selectVKControlBootstrap(listOf(wb, invalid), "")?.id)
	}
}
