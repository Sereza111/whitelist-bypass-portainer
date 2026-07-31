package bypass.whitelist

import android.animation.AnimatorSet
import android.animation.ObjectAnimator
import android.animation.ValueAnimator
import android.content.Intent
import android.os.Build
import android.os.Bundle
import android.view.View
import android.view.animation.LinearInterpolator
import android.widget.ImageView
import android.widget.LinearLayout
import android.widget.TextView
import androidx.activity.enableEdgeToEdge
import androidx.appcompat.app.AppCompatActivity
import androidx.core.content.ContextCompat
import androidx.core.view.ViewCompat
import androidx.core.view.WindowInsetsCompat
import androidx.recyclerview.widget.RecyclerView
import androidx.viewpager2.widget.ViewPager2
import bypass.whitelist.util.Prefs
import com.google.android.material.button.MaterialButton
import kotlin.math.abs

class OnboardingActivity : AppCompatActivity() {

	private data class Page(
		val step: Int,
		val icon: Int,
		val title: Int,
		val body: Int,
	)

	private val pages = listOf(
		Page(1, R.drawable.ic_paste, R.string.onboarding_pair_title, R.string.onboarding_pair_body),
		Page(2, R.drawable.ic_setting_headless, R.string.onboarding_creator_title, R.string.onboarding_creator_body),
		Page(3, R.drawable.ic_power, R.string.onboarding_connect_title, R.string.onboarding_connect_body),
		Page(4, R.drawable.ic_check, R.string.onboarding_recovery_title, R.string.onboarding_recovery_body),
	)

	private lateinit var pager: ViewPager2
	private lateinit var dots: LinearLayout
	private lateinit var action: MaterialButton
	private lateinit var skip: TextView
	private var heroAnimation: AnimatorSet? = null

	override fun onCreate(savedInstanceState: Bundle?) {
		super.onCreate(savedInstanceState)
		enableEdgeToEdge()
		setContentView(R.layout.activity_onboarding)

		val root = findViewById<View>(R.id.onboardingRoot)
		val content = findViewById<View>(R.id.onboardingContent)
		val baseTop = content.paddingTop
		val baseBottom = content.paddingBottom
		ViewCompat.setOnApplyWindowInsetsListener(root) { _, insets ->
			val bars = insets.getInsets(WindowInsetsCompat.Type.systemBars())
			content.setPadding(bars.left, baseTop + bars.top, bars.right, baseBottom + bars.bottom)
			insets
		}

		pager = findViewById(R.id.onboardingPager)
		dots = findViewById(R.id.onboardingDots)
		action = findViewById(R.id.onboardingAction)
		skip = findViewById(R.id.onboardingSkip)
		pager.adapter = PageAdapter()
		pager.offscreenPageLimit = 1
		pager.setPageTransformer { page, position ->
			if (!animationsEnabled()) {
				page.alpha = 1f
				page.scaleX = 1f
				page.scaleY = 1f
				return@setPageTransformer
			}
			val distance = abs(position).coerceAtMost(1f)
			page.alpha = 1f - distance * 0.58f
			page.scaleX = 1f - distance * 0.07f
			page.scaleY = 1f - distance * 0.07f
			page.translationX = -position * page.width * 0.08f
		}

		buildDots()
		pager.registerOnPageChangeCallback(object : ViewPager2.OnPageChangeCallback() {
			override fun onPageSelected(position: Int) {
				updateControls(position)
				pager.post { animateHero(position) }
			}
		})
		skip.setOnClickListener { finishGuide() }
		action.setOnClickListener {
			val next = pager.currentItem + 1
			if (next < pages.size) pager.setCurrentItem(next, animationsEnabled()) else finishGuide()
		}
		updateControls(0)
		if (animationsEnabled()) {
			root.alpha = 0f
			root.translationY = 18f * resources.displayMetrics.density
			root.animate().alpha(1f).translationY(0f).setDuration(420L).start()
		}
	}

	override fun onDestroy() {
		heroAnimation?.cancel()
		super.onDestroy()
	}

	private fun finishGuide() {
		Prefs.onboardingCompleted = true
		setResult(RESULT_OK, Intent())
		finish()
	}

	@Deprecated("Deprecated in Java")
	override fun onBackPressed() {
		if (intent.getBooleanExtra(EXTRA_REPLAY, false)) {
			super.onBackPressed()
		} else {
			finishGuide()
		}
	}

	private fun buildDots() {
		dots.removeAllViews()
		repeat(pages.size) {
			dots.addView(View(this).apply {
				layoutParams = LinearLayout.LayoutParams(dp(6), dp(6)).apply {
					marginStart = dp(4)
					marginEnd = dp(4)
				}
				setBackgroundResource(R.drawable.bg_onboarding_dot_idle)
			})
		}
	}

	private fun updateControls(position: Int) {
		val last = position == pages.lastIndex
		action.setText(if (last) R.string.onboarding_start else R.string.onboarding_next)
		skip.visibility = if (last) View.INVISIBLE else View.VISIBLE
		for (index in 0 until dots.childCount) {
			val dot = dots.getChildAt(index)
			dot.layoutParams = (dot.layoutParams as LinearLayout.LayoutParams).apply {
				width = dp(if (index == position) 22 else 6)
			}
			dot.setBackgroundResource(
				if (index == position) R.drawable.bg_onboarding_dot_active else R.drawable.bg_onboarding_dot_idle,
			)
		}
	}

	private fun animateHero(position: Int) {
		heroAnimation?.cancel()
		val page = pager.findViewWithTag<View>("onboarding-page-$position") ?: return
		val outer = page.findViewById<View>(R.id.onboardingOuterRing)
		val middle = page.findViewById<View>(R.id.onboardingMiddleRing)
		if (!animationsEnabled()) {
			outer.rotation = 0f
			middle.alpha = 1f
			return
		}
		val rotation = ObjectAnimator.ofFloat(outer, View.ROTATION, 0f, 360f).apply {
			duration = 18_000L
			repeatCount = ValueAnimator.INFINITE
			interpolator = LinearInterpolator()
		}
		val scaleX = ObjectAnimator.ofFloat(middle, View.SCALE_X, 0.94f, 1.06f).apply {
			duration = 1_900L
			repeatCount = ValueAnimator.INFINITE
			repeatMode = ValueAnimator.REVERSE
		}
		val scaleY = ObjectAnimator.ofFloat(middle, View.SCALE_Y, 0.94f, 1.06f).apply {
			duration = 1_900L
			repeatCount = ValueAnimator.INFINITE
			repeatMode = ValueAnimator.REVERSE
		}
		heroAnimation = AnimatorSet().also {
			it.playTogether(rotation, scaleX, scaleY)
			it.start()
		}
	}

	private fun animationsEnabled(): Boolean =
		Build.VERSION.SDK_INT < Build.VERSION_CODES.O || ValueAnimator.areAnimatorsEnabled()

	private fun dp(value: Int): Int = (value * resources.displayMetrics.density).toInt()

	private inner class PageAdapter : RecyclerView.Adapter<PageViewHolder>() {
		override fun onCreateViewHolder(parent: android.view.ViewGroup, viewType: Int): PageViewHolder =
			PageViewHolder(layoutInflater.inflate(R.layout.item_onboarding_page, parent, false))

		override fun getItemCount(): Int = pages.size

		override fun onBindViewHolder(holder: PageViewHolder, position: Int) {
			holder.bind(pages[position], position)
		}
	}

	private inner class PageViewHolder(itemView: View) : RecyclerView.ViewHolder(itemView) {
		fun bind(page: Page, position: Int) {
			itemView.tag = "onboarding-page-$position"
			itemView.findViewById<TextView>(R.id.onboardingStep).text =
				getString(R.string.onboarding_step, page.step, pages.size)
			itemView.findViewById<TextView>(R.id.onboardingTitle).setText(page.title)
			itemView.findViewById<TextView>(R.id.onboardingBody).setText(page.body)
			itemView.findViewById<ImageView>(R.id.onboardingIcon).apply {
				setImageResource(page.icon)
				setColorFilter(ContextCompat.getColor(this@OnboardingActivity, R.color.accent_emerald))
			}
		}
	}

	companion object {
		const val EXTRA_REPLAY = "onboarding_replay"
	}
}
