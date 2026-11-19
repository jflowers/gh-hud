package io.github.jflowers.ghhud

import androidx.activity.compose.BackHandler
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.padding
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.List
import androidx.compose.material.icons.filled.Build
import androidx.compose.material.icons.filled.Notifications
import androidx.compose.material.icons.filled.Person
import androidx.compose.material.icons.filled.PlayArrow
import androidx.compose.material.icons.filled.Settings
import androidx.compose.material3.Badge
import androidx.compose.material3.BadgedBox
import androidx.compose.material3.Icon
import androidx.compose.material3.NavigationBar
import androidx.compose.material3.NavigationBarItem
import androidx.compose.material3.Scaffold
import androidx.compose.material3.SnackbarDuration
import androidx.compose.material3.SnackbarHost
import androidx.compose.material3.SnackbarHostState
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.compose.ui.platform.testTag
import io.github.jflowers.ghhud.core.AgentList
import io.github.jflowers.ghhud.core.FleetList
import io.github.jflowers.ghhud.core.FleetSort
import io.github.jflowers.ghhud.core.ItemCommands
import io.github.jflowers.ghhud.core.WatchTarget
import io.github.jflowers.ghhud.core.WorkspaceList
import io.github.jflowers.ghhud.core.ItemFilter
import io.github.jflowers.ghhud.core.ItemList
import io.github.jflowers.ghhud.core.PlanList
import io.github.jflowers.ghhud.core.ToolkitList
import kotlinx.coroutines.launch

/** The places in the bottom bar. More come with Plan, Agents and Fleet. */
enum class Place(val label: String, val icon: ImageVector) {
    INBOX("Inbox", Icons.Filled.Notifications),
    ITEMS("Items", Icons.AutoMirrored.Filled.List),
    PLAN("Plan", Icons.Filled.PlayArrow),
    AGENTS("Agents", Icons.Filled.Person),

    /** Only while the hub runs a fleet, as on the Mac. */
    FLEET("Fleet", Icons.Filled.Build),

    /** Only while the hub runs the toolkit, as on the Mac. */
    TOOLS("Tools", Icons.Filled.Settings),
}

/** Everything the app's screens can ask for. */
interface AppActions : AttentionActions, ItemsControl, ItemActions, PlanActions, AgentsControl, AgentActions, FleetActions, ViewerActions, ToolkitActions

/**
 * The app: a bottom bar between the places, each a list; an item's row
 * opens its own screen, and Back returns to the list.
 */
@Composable
fun AppScreen(
    ui: Ui,
    items: ItemsView,
    agents: AgentsView,
    fleet: ListState<FleetSort>,
    act: AppActions,
    shared: String? = null,
    onShared: () -> Unit = {},
    checkout: String? = null,
    onCheckout: () -> Unit = {},
) {
    var place by rememberSaveable { mutableStateOf(Place.INBOX) }
    // Start an agent on an item you name: the field's first text, or null
    // while closed. A link shared to the app opens it.
    var named by rememberSaveable { mutableStateOf<String?>(null) }
    LaunchedEffect(shared) {
        if (shared != null) {
            named = shared
            onShared()
        }
    }
    // The open detail screen, if any: "item:<url>", "agent:<id>", "ws:<id>",
    // "wsref:<[machine:]workspace>" (from a notification),
    // "watch:<run:id|session:id>" or "transcript:<run id>"; the viewers'
    // titles ride along in viewTitle.
    var detail by rememberSaveable { mutableStateOf<String?>(null) }
    // The rollout the Tools screen opens on, when the inbox sent you there.
    var openRollout by rememberSaveable { mutableStateOf<String?>(null) }
    var viewTitle by rememberSaveable { mutableStateOf("") }
    // Back from a viewer returns to the screen that opened it.
    var under by rememberSaveable { mutableStateOf<String?>(null) }
    val watch: (WatchTarget) -> Unit = {
        under = detail
        viewTitle = it.title
        detail = "watch:" + it.key
    }
    val transcript: (String, String) -> Unit = { id, title ->
        under = detail
        viewTitle = title
        detail = "transcript:$id"
    }
    val snackbar = remember { SnackbarHostState() }
    val scope = rememberCoroutineScope()
    val say: Say = { text, bad ->
        scope.launch {
            snackbar.currentSnackbarData?.dismiss()
            snackbar.showSnackbar(
                text,
                withDismissAction = bad,
                duration = if (bad) SnackbarDuration.Indefinite else SnackbarDuration.Short,
            )
        }
    }
    val goBack = {
        val d = detail
        detail = if (d != null && (d.startsWith("watch:") || d.startsWith("transcript:"))) under else null
        if (detail == null) under = null
    }
    BackHandler(enabled = detail != null) { goBack() }
    LaunchedEffect(checkout) {
        if (checkout != null) {
            place = Place.AGENTS
            under = null
            detail = "wsref:$checkout"
            onCheckout()
        }
    }
    Scaffold(
        snackbarHost = { SnackbarHost(snackbar) },
        bottomBar = {
            NavigationBar {
                val fleetOn = ui.hub.snapshot.hasFleet()
                val toolsOn = ui.hub.snapshot.hasToolkit()
                for (p in Place.entries.filter { (it != Place.FLEET || fleetOn) && (it != Place.TOOLS || toolsOn) }) {
                    NavigationBarItem(
                        selected = p == place,
                        onClick = {
                            place = p
                            detail = null
                        },
                        icon = {
                            val snap = ui.hub.snapshot
                            val badge = when (p) {
                                Place.INBOX -> ui.hub.attention.count.takeIf { it > 0 }?.toString()
                                Place.PLAN -> PlanList.starting(snap).takeIf { it > 0 }?.toString()
                                Place.AGENTS -> AgentList.running(snap).takeIf { it > 0 }?.toString()
                                Place.FLEET -> FleetList.progress(snap) // "2/6": machines on the target
                                Place.TOOLS -> ToolkitList.waiting(snap).takeIf { it > 0 }?.toString()
                                Place.ITEMS -> null
                            }
                            BadgedBox(badge = { badge?.let { Badge { Text(it) } } }) { Icon(p.icon, contentDescription = null) }
                        },
                        label = { Text(p.label) },
                        modifier = Modifier.testTag("place:" + p.name),
                    )
                }
            }
        },
    ) { inner ->
        Box(Modifier.padding(inner)) {
            val d = detail
            val s = ui.hub.snapshot
            val off = ItemCommands.off(ui.hub)
            val back = goBack
            when {
                d != null && d.startsWith("item:") -> {
                    val id = d.removePrefix("item:")
                    val row = remember(s, id) { ItemList.rows(s, ItemFilter.ALL).firstOrNull { it.id == id } }
                    ItemScreen(row, off, act, say, onBack = back, onWatch = watch, onTranscript = transcript)
                }
                d != null && d.startsWith("agent:") -> {
                    val id = d.removePrefix("agent:")
                    val row = remember(s, id) { AgentList.rows(s, showStale = true).firstOrNull { it.id == id } }
                    AgentScreen(row, off, act, act, say, onBack = back, onWatch = watch, onTranscript = transcript)
                }
                d != null && d.startsWith("watch:") -> {
                    val t = WatchTarget.parse(d.removePrefix("watch:"), viewTitle)
                    if (t != null) WatchScreen(t, act, onBack = back)
                }
                d != null && d.startsWith("transcript:") -> TranscriptScreen(d.removePrefix("transcript:"), viewTitle, act, onBack = back)
                d != null && d.startsWith("wsref:") -> {
                    val ref = d.removePrefix("wsref:")
                    val row = remember(s, ref) { WorkspaceList.byRef(s, ref) }
                    WorkspaceScreen(row, act, onBack = back)
                }
                d != null && d.startsWith("ws:") -> {
                    val id = d.removePrefix("ws:")
                    val row = remember(s, id) { WorkspaceList.rows(s).firstOrNull { it.id == id } }
                    WorkspaceScreen(row, act, onBack = back)
                }
                place == Place.INBOX -> AttentionScreen(ui, act, onToolkit = { rollout ->
                    openRollout = rollout
                    place = Place.TOOLS
                })
                place == Place.ITEMS -> ItemsScreen(ui, items, act, onOpenItem = { detail = "item:" + it.id }, onNamed = { named = "" })
                place == Place.PLAN -> PlanScreen(ui, act, act, say, refresh = act::refresh)
                place == Place.AGENTS -> AgentsScreen(
                    ui,
                    agents,
                    act,
                    onOpenAgent = { detail = "agent:" + it.id },
                    onOpenWorkspace = { detail = "ws:" + it.id },
                )
                place == Place.FLEET && s.hasFleet() -> FleetScreen(ui, fleet, act, say, refresh = act::refresh)
                place == Place.FLEET -> AttentionScreen(ui, act) // the fleet went off: back to the inbox
                place == Place.TOOLS && s.hasToolkit() -> ToolkitScreen(ui, act, say, refresh = act::refresh, open = openRollout)
                place == Place.TOOLS -> AttentionScreen(ui, act) // the toolkit went off: back to the inbox
            }
        }
    }
    named?.let { initial ->
        NamedAgentSheet(initial, act, onDone = { said ->
            named = null
            said?.let { say(it, false) }
        })
    }
}
