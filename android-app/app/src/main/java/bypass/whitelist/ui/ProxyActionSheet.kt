package bypass.whitelist.ui

import android.content.ClipData
import android.content.ClipboardManager
import android.content.Context
import android.os.Bundle
import android.view.LayoutInflater
import android.view.View
import android.view.ViewGroup
import android.widget.EditText
import android.widget.ImageView
import android.widget.LinearLayout
import android.widget.TextView
import android.widget.Toast
import androidx.fragment.app.FragmentManager
import androidx.core.widget.doAfterTextChanged
import bypass.whitelist.R
import bypass.whitelist.util.Callback
import bypass.whitelist.util.LanProxy
import bypass.whitelist.util.Prefs
import bypass.whitelist.util.SocksAuth
import bypass.whitelist.util.SocksAuthMode
import com.google.android.material.bottomsheet.BottomSheetDialogFragment
import com.google.android.material.button.MaterialButton
import com.google.android.material.materialswitch.MaterialSwitch

class ProxyActionSheet : BottomSheetDialogFragment() {

    private var onSaved: Callback? = null
    private var selectedAuth: SocksAuthMode = SocksAuthMode.AUTO

    override fun onCreateView(
        inflater: LayoutInflater,
        container: ViewGroup?,
        savedInstanceState: Bundle?,
    ): View = inflater.inflate(R.layout.sheet_action_proxy, container, false)

    override fun onViewCreated(view: View, savedInstanceState: Bundle?) {
		selectedAuth = Prefs.socksAuthMode
		view.findViewById<View>(R.id.proxyPortCard).clipToOutline = true
		view.findViewById<View>(R.id.proxyLanCard).clipToOutline = true
		view.findViewById<View>(R.id.proxyLanDetails).clipToOutline = true
		val portInput = view.findViewById<EditText>(R.id.proxyPortInput)
		val authContainer = view.findViewById<LinearLayout>(R.id.proxyAuthContainer)
        val manualContainer = view.findViewById<LinearLayout>(R.id.proxyManualContainer)
        val userInput = view.findViewById<EditText>(R.id.proxyUserInput)
        val passInput = view.findViewById<EditText>(R.id.proxyPassInput)
		val proxyOnly = view.findViewById<MaterialSwitch>(R.id.proxyOnlySwitch)
		val lanSwitch = view.findViewById<MaterialSwitch>(R.id.proxyLanSwitch)
		val lanDetails = view.findViewById<LinearLayout>(R.id.proxyLanDetails)
		val lanEndpoint = view.findViewById<TextView>(R.id.proxyLanEndpoint)
		val lanCredentials = view.findViewById<TextView>(R.id.proxyLanCredentials)

		portInput.setText(Prefs.socksPort.toString())
		userInput.setText(Prefs.socksUser)
		passInput.setText(Prefs.socksPass)
		proxyOnly.isChecked = Prefs.proxyOnly
		lanSwitch.isChecked = Prefs.allowLan
		manualContainer.visibility = if (selectedAuth == SocksAuthMode.MANUAL) View.VISIBLE else View.GONE

		fun selectedCredentials(): Pair<String, String> {
			if (selectedAuth == SocksAuthMode.MANUAL) {
				val user = userInput.text.toString().trim()
				val pass = passInput.text.toString()
				if (user.isNotBlank() && pass.isNotBlank()) return user to pass
			}
			return SocksAuth.generatedUser to SocksAuth.generatedPass
		}

		fun refreshLanDetails() {
			lanDetails.visibility = if (lanSwitch.isChecked) View.VISIBLE else View.GONE
			if (!lanSwitch.isChecked) return
			val port = portInput.text.toString().toLongOrNull()?.takeIf { it in 1L..65535L }
				?: Prefs.socksPort
			val endpoints = LanProxy.endpoints(port)
			lanEndpoint.text = if (endpoints.isEmpty()) {
				getString(R.string.proxy_lan_no_address)
			} else {
				endpoints.joinToString("\n")
			}
			val (user, pass) = selectedCredentials()
			lanCredentials.text = getString(R.string.proxy_lan_credentials, user, pass)
		}

		lanSwitch.setOnCheckedChangeListener { _, _ -> refreshLanDetails() }
		view.findViewById<View>(R.id.proxyLanCard).setOnClickListener {
			lanSwitch.isChecked = !lanSwitch.isChecked
		}
		portInput.doAfterTextChanged { refreshLanDetails() }
		userInput.doAfterTextChanged { refreshLanDetails() }
		passInput.doAfterTextChanged { refreshLanDetails() }

        val inflater = LayoutInflater.from(requireContext())
        val rowViews = mutableMapOf<SocksAuthMode, View>()
        val context = requireContext()
        SocksAuthMode.entries.forEach { mode ->
            val row = inflater.inflate(R.layout.item_action_option, authContainer, false)
            row.clipToOutline = true
            val title = when (mode) {
                SocksAuthMode.AUTO -> context.getString(R.string.proxy_auth_auto)
                SocksAuthMode.MANUAL -> context.getString(R.string.proxy_auth_manual)
            }
            val sub = when (mode) {
                SocksAuthMode.AUTO -> context.getString(R.string.proxy_auth_auto_sub)
                SocksAuthMode.MANUAL -> context.getString(R.string.proxy_auth_manual_sub)
            }
            row.findViewById<TextView>(R.id.actionOptionTitle).text = title
            row.findViewById<TextView>(R.id.actionOptionSub).apply {
                text = sub
                visibility = View.VISIBLE
            }
			row.setOnClickListener {
				selectedAuth = mode
				updateAuthSelection(rowViews)
				manualContainer.visibility = if (mode == SocksAuthMode.MANUAL) View.VISIBLE else View.GONE
				refreshLanDetails()
			}
            authContainer.addView(row)
            rowViews[mode] = row
        }
		updateAuthSelection(rowViews)
		refreshLanDetails()

		view.findViewById<MaterialButton>(R.id.proxyCopyLanButton).setOnClickListener {
			val port = portInput.text.toString().toLongOrNull()?.takeIf { it in 1L..65535L }
				?: Prefs.socksPort
			val endpoints = LanProxy.endpoints(port)
			val (user, pass) = selectedCredentials()
			val text = buildString {
				appendLine("WhitelistBypass phone SOCKS5")
				appendLine("Host: ${endpoints.firstOrNull()?.substringBeforeLast(':') ?: "<phone LAN IP>"}")
				appendLine("Port: $port")
				appendLine("User: $user")
				append("Password: $pass")
			}
			val clipboard = requireContext().getSystemService(Context.CLIPBOARD_SERVICE) as ClipboardManager
			clipboard.setPrimaryClip(ClipData.newPlainText("WhitelistBypass SOCKS5", text))
			Toast.makeText(requireContext(), R.string.proxy_lan_copied, Toast.LENGTH_SHORT).show()
		}

        view.findViewById<MaterialButton>(R.id.proxyCancelButton).setOnClickListener { dismiss() }
		view.findViewById<MaterialButton>(R.id.proxySaveButton).setOnClickListener {
			if (lanSwitch.isChecked && selectedAuth == SocksAuthMode.MANUAL &&
				(userInput.text.isNullOrBlank() || passInput.text.isNullOrBlank())
			) {
				Toast.makeText(requireContext(), R.string.proxy_lan_manual_auth_required, Toast.LENGTH_LONG).show()
				return@setOnClickListener
			}
			portInput.text.toString().toLongOrNull()?.takeIf { it in 1L..65535L }?.let { Prefs.socksPort = it }
			Prefs.allowLan = lanSwitch.isChecked
			Prefs.socksHost = Prefs.effectiveSocksHost
            Prefs.socksAuthMode = selectedAuth
            if (selectedAuth == SocksAuthMode.MANUAL) {
                Prefs.socksUser = userInput.text.toString().trim()
                Prefs.socksPass = passInput.text.toString()
            }
            Prefs.proxyOnly = proxyOnly.isChecked
            onSaved?.invoke()
            dismiss()
        }
    }

    private fun updateAuthSelection(rowViews: Map<SocksAuthMode, View>) {
        val context = requireContext()
        rowViews.forEach { (mode, row) ->
            val isActive = mode == selectedAuth
            val titleView = row.findViewById<TextView>(R.id.actionOptionTitle)
            val checkBox = row.findViewById<View>(R.id.actionOptionCheckBox)
            val checkIcon = row.findViewById<ImageView>(R.id.actionOptionCheckIcon)
            if (isActive) {
                row.setBackgroundResource(R.drawable.bg_destination_card_active)
                checkBox.setBackgroundResource(R.drawable.bg_action_check_active)
                checkIcon.visibility = View.VISIBLE
                titleView.setTextColor(context.getColor(R.color.accent_emerald))
            } else {
                row.setBackgroundResource(R.drawable.bg_destination_card)
                checkBox.setBackgroundResource(R.drawable.bg_action_check_idle)
                checkIcon.visibility = View.GONE
                titleView.setTextColor(context.getColor(R.color.ink))
            }
        }
    }

    companion object {
        fun show(manager: FragmentManager, onSaved: Callback) {
            ProxyActionSheet().apply { this.onSaved = onSaved }.show(manager, "ProxyActionSheet")
        }
    }
}
