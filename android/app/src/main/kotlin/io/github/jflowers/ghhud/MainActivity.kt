package io.github.jflowers.ghhud

import android.Manifest
import android.content.ActivityNotFoundException
import android.content.ClipData
import android.content.ClipboardManager
import android.content.Intent
import android.content.pm.PackageManager
import android.os.Build
import android.os.Bundle
import android.widget.Toast
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.activity.enableEdgeToEdge
import androidx.activity.result.contract.ActivityResultContracts
import androidx.activity.viewModels
import androidx.compose.foundation.isSystemInDarkTheme
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.darkColorScheme
import androidx.compose.material3.lightColorScheme
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.core.net.toUri
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import io.github.jflowers.ghhud.api.v1.FleetAction
import io.github.jflowers.ghhud.api.v1.RefreshTarget
import io.github.jflowers.ghhud.api.v1.ToolkitAction
import io.github.jflowers.ghhud.core.RolloutRow
import io.github.jflowers.ghhud.core.ToolkitCommands
import io.github.jflowers.ghhud.core.FleetCommands
import io.github.jflowers.ghhud.core.DeweyCommands
import io.github.jflowers.ghhud.core.Viewers
import io.github.jflowers.ghhud.core.WatchTarget
import io.github.jflowers.ghhud.core.FleetSort
import io.github.jflowers.ghhud.core.AgentChoice
import io.github.jflowers.ghhud.core.AgentCommands
import io.github.jflowers.ghhud.core.AgentRow
import io.github.jflowers.ghhud.core.AttentionRow
import io.github.jflowers.ghhud.core.CaseRow
import io.github.jflowers.ghhud.core.EpicRow
import io.github.jflowers.ghhud.core.PlanCommands
import io.github.jflowers.ghhud.core.PlanItem
import io.github.jflowers.ghhud.core.ItemCommands
import io.github.jflowers.ghhud.core.ItemRow
import io.github.jflowers.ghhud.core.NamedItem
import io.github.jflowers.ghhud.core.HubAddress

/**
 * The app: what needs you, and the items (AppScreen). Launching it with the extras
 * hub_url (and hub_token, for the Mac's loopback hub) sets the hub, which
 * is how `make android-dev-hub` points it at the Mac over USB.
 */
class MainActivity : ComponentActivity() {
    private val model: HubModel by viewModels()

    private val actions = object : AppActions {
        override fun update(change: (ItemsView) -> ItemsView) = model.updateItems(change)

        override fun keepTerms() = model.keepTerms()

        override fun copy(text: String) {
            getSystemService(ClipboardManager::class.java).setPrimaryClip(ClipData.newPlainText("link", text))
        }

        override suspend fun choices(row: ItemRow, queue: Boolean) = ItemCommands.choices(model.connection, row, queue)

        override suspend fun run(row: ItemRow, c: AgentChoice, queue: Boolean) = ItemCommands.run(model.connection, row, c, queue)

        override suspend fun recheck(row: ItemRow) = ItemCommands.recheck(model.connection, row)

        override suspend fun lookUp(text: String) = ItemCommands.lookUp(model.connection, text)

        override suspend fun run(item: NamedItem, c: AgentChoice) = ItemCommands.run(model.connection, item, c)

        override fun updateAgents(change: (AgentsView) -> AgentsView) = model.updateAgents(change)

        override fun refreshAgents() = model.refresh(RefreshTarget.REFRESH_TARGET_AGENTS)

        override fun refreshWorkspaces() = model.refresh(RefreshTarget.REFRESH_TARGET_WORKSPACES)

        override suspend fun recheck(r: AgentRow) = AgentCommands.recheck(model.connection, r)

        override suspend fun fleet(action: FleetAction) = FleetCommands.run(model.connection, action, model.ui.value.hub.snapshot.fleet)

        override suspend fun dewey(machine: String?, force: Boolean) = DeweyCommands.start(model.connection, machine, force)

        override suspend fun deweyCancel(machine: String?) = DeweyCommands.cancel(model.connection, machine)

        override fun updateFleet(change: (ListState<FleetSort>) -> ListState<FleetSort>) = model.updateFleet(change)

        override suspend fun checkTools() = ToolkitCommands.check(model.connection)

        override suspend fun rollOut(tool: String, version: String) = ToolkitCommands.rollOut(model.connection, tool, version)

        override suspend fun decline(id: String) = ToolkitCommands.decline(model.connection, id)

        override suspend fun toolkit(action: ToolkitAction, r: RolloutRow) = ToolkitCommands.control(model.connection, action, r)

        override fun follow(t: WatchTarget) = Viewers.follow(model.connection, t)

        override suspend fun transcript(runId: String) = model.connection.transcript(runId)

        private val plan get() = PlanCommands(model.connection)

        override suspend fun enqueue(r: PlanItem) = plan.enqueue(r)

        override suspend fun move(r: PlanItem, by: Long) = plan.move(r, by)

        override suspend fun unqueue(r: PlanItem) = plan.unqueue(r)

        override suspend fun approve(c: CaseRow) = plan.approve(c)

        override suspend fun reject(c: CaseRow) = plan.reject(c)

        override suspend fun approve(e: EpicRow) = plan.approve(e)

        override suspend fun reread(e: EpicRow) = plan.reread(e)

        override suspend fun drop(e: EpicRow) = plan.drop(e)

        override suspend fun clearGate(epic: String, piece: String) = plan.clearGate(epic, piece)

        override suspend fun enroll(target: String) = plan.enroll(target)

        override fun open(url: String) {
            try {
                startActivity(Intent(Intent.ACTION_VIEW, url.toUri()))
            } catch (_: ActivityNotFoundException) {
                Toast.makeText(this@MainActivity, "Nothing can open $url", Toast.LENGTH_SHORT).show()
            }
        }

        override fun approve(row: AttentionRow) = model.approve(row)

        override fun reject(row: AttentionRow) = model.reject(row)

        override fun refresh() = model.refresh()

        override fun setAddress(a: HubAddress) = model.setAddress(a)

        override fun setNotify(on: Boolean) {
            val asked = Build.VERSION.SDK_INT >= 33 &&
                checkSelfPermission(Manifest.permission.POST_NOTIFICATIONS) != PackageManager.PERMISSION_GRANTED
            if (on && asked) permission.launch(Manifest.permission.POST_NOTIFICATIONS) else model.setNotify(on)
        }
    }

    // Notifying needs the permission (Android 13 and later); without it the
    // switch stays off.
    private val permission = registerForActivityResult(ActivityResultContracts.RequestPermission()) { granted ->
        if (granted) {
            model.setNotify(true)
        } else {
            Toast.makeText(this, "gh-hud can't notify you without the permission", Toast.LENGTH_LONG).show()
        }
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        enableEdgeToEdge()
        takeAddress(intent)
        takeShared(intent)
        takeCheckout(intent)
        setContent {
            MaterialTheme(colorScheme = if (isSystemInDarkTheme()) darkColorScheme() else lightColorScheme()) {
                val ui by model.ui.collectAsStateWithLifecycle()
                val items by model.items.collectAsStateWithLifecycle()
                val agents by model.agents.collectAsStateWithLifecycle()
                val fleet by model.fleet.collectAsStateWithLifecycle()
                AppScreen(
                    ui, items, agents, fleet, actions, shared = shared, onShared = { shared = null },
                    checkout = checkout, onCheckout = { checkout = null },
                )
            }
        }
    }

    override fun onNewIntent(intent: Intent) {
        super.onNewIntent(intent)
        takeAddress(intent)
        takeShared(intent)
        takeCheckout(intent)
    }

    // A failed reindex's or upkeep's notification opens its checkout.
    private var checkout by mutableStateOf<String?>(null)

    private fun takeCheckout(intent: Intent) {
        checkout = intent.getStringExtra(EXTRA_CHECKOUT) ?: return
    }

    // A GitHub issue or PR link shared to gh-hud (from a browser or the
    // GitHub app) opens Start an agent with it.
    private var shared by mutableStateOf<String?>(null)

    private fun takeShared(intent: Intent) {
        if (intent.action != Intent.ACTION_SEND) return
        val text = intent.getStringExtra(Intent.EXTRA_TEXT) ?: return
        shared = ItemCommands.sharedLink(text) ?: text.trim()
    }

    private fun takeAddress(intent: Intent) {
        val url = intent.getStringExtra(EXTRA_URL) ?: return
        model.setAddress(HubAddress(url, intent.getStringExtra(EXTRA_TOKEN).orEmpty()))
    }

    // The screen holds the subscription while it's on screen; the service,
    // when you asked to be notified, holds it the rest of the time.
    override fun onStart() {
        super.onStart()
        model.start()
    }

    override fun onStop() {
        model.stop()
        super.onStop()
    }

    companion object {
        const val EXTRA_URL = "hub_url"
        const val EXTRA_TOKEN = "hub_token"
        const val EXTRA_CHECKOUT = "checkout"
    }
}
