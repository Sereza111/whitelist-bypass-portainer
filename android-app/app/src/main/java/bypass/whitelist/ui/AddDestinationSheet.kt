package bypass.whitelist.ui

import android.content.ClipboardManager
import android.content.Context
import android.os.Bundle
import android.view.LayoutInflater
import android.view.View
import android.view.ViewGroup
import android.widget.Button
import android.widget.EditText
import android.widget.LinearLayout
import android.widget.TextView
import android.widget.Toast
import androidx.fragment.app.FragmentManager
import com.google.android.material.bottomsheet.BottomSheetDialogFragment
import bypass.whitelist.R
import bypass.whitelist.tunnel.CallConfig
import bypass.whitelist.util.Prefs

class AddDestinationSheet : BottomSheetDialogFragment() {

    override fun onCreateView(
        inflater: LayoutInflater,
        container: ViewGroup?,
        savedInstanceState: Bundle?,
    ): View = inflater.inflate(R.layout.sheet_add_destination, container, false)

    override fun onViewCreated(view: View, savedInstanceState: Bundle?) {
        val inputName = view.findViewById<EditText>(R.id.inputName)
        val inputLink = view.findViewById<EditText>(R.id.inputLink)
		val inputRecoveryProfile = view.findViewById<EditText>(R.id.inputRecoveryProfile)
		val inputRecoveryKey = view.findViewById<EditText>(R.id.inputRecoveryKey)
        val pasteChip = view.findViewById<LinearLayout>(R.id.pasteChip)
        val pasteChipLabel = view.findViewById<TextView>(R.id.pasteChipLabel)
        val buttonCancel = view.findViewById<Button>(R.id.buttonCancel)
        val buttonSave = view.findViewById<Button>(R.id.buttonSave)

        pasteChip.setOnClickListener {
            val clipboard = requireContext().getSystemService(Context.CLIPBOARD_SERVICE) as ClipboardManager
            val clip = clipboard.primaryClip
            val text = clip?.takeIf { it.itemCount > 0 }?.getItemAt(0)?.coerceToText(requireContext())?.toString().orEmpty().trim()
            if (text.isEmpty()) {
                Toast.makeText(requireContext(), R.string.clipboard_empty, Toast.LENGTH_SHORT).show()
                return@setOnClickListener
            }
			val recovery = parseRecoveryBlock(text)
			if (recovery != null) {
				inputName.setText(recovery.name)
				inputLink.setText(recovery.link)
				inputRecoveryProfile.setText(recovery.profile)
				inputRecoveryKey.setText(recovery.key)
				recoveryGeneration = recovery.generation
			} else {
				inputLink.setText(text)
			}
            if (inputName.text.toString().trim().isEmpty()) {
                inputName.setText(CallConfig.suggestNameFor(text))
            }
            flashChip(pasteChip, pasteChipLabel)
        }

        buttonCancel.setOnClickListener { dismiss() }

        buttonSave.setOnClickListener {
            val link = inputLink.text.toString().trim()
            if (link.isEmpty()) {
                inputLink.requestFocus()
                return@setOnClickListener
            }
            val name = inputName.text.toString().trim().ifEmpty { CallConfig.suggestNameFor(link) }
			val profile = inputRecoveryProfile.text.toString().trim()
			val key = inputRecoveryKey.text.toString().trim()
			if ((profile.isEmpty()) != (key.isEmpty())) {
				Toast.makeText(requireContext(), R.string.recovery_pairing_incomplete, Toast.LENGTH_LONG).show()
				return@setOnClickListener
			}
			val config = CallConfig.newWith(name = name, url = link).copy(
				recoveryProfile = profile.ifEmpty { null },
				recoveryKey = key.ifEmpty { null },
				recoveryGeneration = recoveryGeneration,
			)
            Prefs.addDestination(config)
            (parentFragment as? CallsListener)?.onDestinationsChanged()
            (activity as? CallsListener)?.onDestinationsChanged()
            (activity as? CallsListener)?.onDestinationSelected(config)
            dismiss()
        }
    }

	private var recoveryGeneration: Int = 0
	private data class RecoveryBlock(val name: String, val profile: String, val key: String, val link: String, val generation: Int)

	private fun parseRecoveryBlock(text: String): RecoveryBlock? {
		if (!text.startsWith("WLB Recovery Profile")) return null
		fun value(label: String): String = text.lineSequence()
			.firstOrNull { it.startsWith("$label:", ignoreCase = true) }
			?.substringAfter(':')?.trim().orEmpty()
		val profile = value("Profile")
		val key = value("Key")
		val link = value("Link")
		if (profile.isBlank() || key.isBlank() || link.isBlank() || link.startsWith("<")) return null
		val generation = value("Generation").toIntOrNull() ?: 0
		return RecoveryBlock(value("Name").ifBlank { "Resilient VK" }, profile, key, link, generation)
	}

    private fun flashChip(chip: LinearLayout, label: TextView) {
        chip.setBackgroundResource(R.drawable.bg_paste_chip_flash)
        label.setTextColor(requireContext().getColor(R.color.accent_emerald))
        chip.postDelayed({
            if (isAdded) {
                chip.setBackgroundResource(R.drawable.bg_paste_chip)
                label.setTextColor(requireContext().getColor(R.color.ink_2))
            }
        }, 320)
    }

    companion object {
        const val TAG = "AddDestinationSheet"

        fun show(manager: FragmentManager) {
            AddDestinationSheet().show(manager, TAG)
        }
    }
}
