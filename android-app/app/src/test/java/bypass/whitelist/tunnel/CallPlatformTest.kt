package bypass.whitelist.tunnel

import org.junit.Assert.assertEquals
import org.junit.Test

class CallPlatformTest {
    @Test
    fun detectsEverySupportedProvider() {
        assertEquals(CallPlatform.VK, CallPlatform.fromUrl("https://vk.com/call/join/example"))
        assertEquals(CallPlatform.TELEMOST, CallPlatform.fromUrl("https://telemost.yandex.ru/j/123456"))
        assertEquals(CallPlatform.WBSTREAM, CallPlatform.fromUrl("wbstream://room-123"))
        assertEquals(CallPlatform.WBSTREAM, CallPlatform.fromUrl("https://stream.wb.ru/room/room-123"))
        assertEquals(CallPlatform.DION, CallPlatform.fromUrl("dion://room-123"))
        assertEquals("room-123", CallPlatform.extractRoomId("https://stream.wb.ru/room/room-123"))
    }

    @Test
    fun validatesProviderBoundInviteLinks() {
        val valid = listOf(
            CallPlatform.VK to "https://vk.com/call/join/example",
            CallPlatform.TELEMOST to "https://telemost.yandex.ru/j/123456",
            CallPlatform.WBSTREAM to "wbstream://room-123",
            CallPlatform.DION to "dion://room-123",
            CallPlatform.DION to "https://dion.vc/event/room-123",
        )
        valid.forEach { (provider, link) ->
            assertEquals("$provider $link", true, CallPlatform.isSafeInviteLink(provider, link))
        }
        assertEquals(false, CallPlatform.isSafeInviteLink(CallPlatform.VK, "http://vk.com/call/join/example"))
        assertEquals(false, CallPlatform.isSafeInviteLink(CallPlatform.TELEMOST, "https://evil.example/j/123456"))
        assertEquals(false, CallPlatform.isSafeInviteLink(CallPlatform.WBSTREAM, "wbstream://room/path"))
        assertEquals(false, CallPlatform.isSafeInviteLink(CallPlatform.DION, "dion://room?token=secret"))
    }
}
