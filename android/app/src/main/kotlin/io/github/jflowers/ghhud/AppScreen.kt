package io.github.jflowers.ghhud

import androidx.activity.compose.BackHandler
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.padding
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.List
import androidx.compose.material.icons.filled.Notifications
import androidx.compose.material.icons.filled.Person
import androidx.compose.material.icons.filled.PlayArrow
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
import io.github.jflowers.ghhud.core.ItemCommands
import io.github.jflowers.ghhud.core.WorkspaceList
import io.github.jflowers.ghhud.core.ItemFilter
import io.github.jflowers.ghhud.core.ItemList
import io.github.jflowers.ghhud.core.PlanList
import kotlinx.coroutines.launch

/** The places in the bottom bar. More come with Plan, Agents and Fleet. */
enum class Place(val label: String, val icon: ImageVector) {
    INBOX("Inbox", Icons.Filled.Notifications),
    ITEMS("Items", Icons.AutoMirrored.Filled.List),
    PLAN("Plan", Icons.Filled.PlayArrow),
    AGENTS("Agents", Icons.Filled.Person),
}

/** Everything the app's screens can ask for. */
interface AppActions : AttentionActions, ItemsControl, ItemActions, PlanActions, AgentsControl, AgentActions

/**
 * The app: a bottom bar between the places, each a list; an item's row
 * opens its own screen, and Back returns to the list.
 */
@Composable
fun AppScreen(ui: Ui, items: ItemsView, agents: AgentsView, act: AppActions) {
    var place by rememberSaveable { mutableStateOf(Place.INBOX) }
    // The open detail screen, if any: "item:<url>", "agent:<id>" or "ws:<id>".
    var detail by rememberSaveable { mutableStateOf<String?>(null) }
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
    BackHandler(enabled = detail != null) { detail = null }
    Scaffold(
        snackbarHost = { SnackbarHost(snackbar) },
        bottomBar = {
            NavigationBar {
                for (p in Place.entries) {
                    NavigationBarItem(
                        selected = p == place,
                        onClick = {
                            place = p
                            detail = null
                        },
                        icon = {
                            val n = when (p) {
                                Place.INBOX -> ui.hub.attention.count
                                Place.PLAN -> PlanList.starting(ui.hub.snapshot)
                                Place.AGENTS -> AgentList.running(ui.hub.snapshot)
                                else -> 0
                            }
                            BadgedBox(badge = { if (n > 0) Badge { Text("$n") } }) { Icon(p.icon, contentDescription = null) }
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
            val back = { detail = null }
            when {
                d != null && d.startsWith("item:") -> {
                    val id = d.removePrefix("item:")
                    val row = remember(s, id) { ItemList.rows(s, ItemFilter.ALL).firstOrNull { it.id == id } }
                    ItemScreen(row, off, act, say, onBack = back)
                }
                d != null && d.startsWith("agent:") -> {
                    val id = d.removePrefix("agent:")
                    val row = remember(s, id) { AgentList.rows(s, showStale = true).firstOrNull { it.id == id } }
                    AgentScreen(row, off, act, act, say, onBack = back)
                }
                d != null && d.startsWith("ws:") -> {
                    val id = d.removePrefix("ws:")
                    val row = remember(s, id) { WorkspaceList.rows(s).firstOrNull { it.id == id } }
                    WorkspaceScreen(row, act, onBack = back)
                }
                place == Place.INBOX -> AttentionScreen(ui, act)
                place == Place.ITEMS -> ItemsScreen(ui, items, act, onOpenItem = { detail = "item:" + it.id })
                place == Place.PLAN -> PlanScreen(ui, act, act, say, refresh = act::refresh)
                place == Place.AGENTS -> AgentsScreen(
                    ui,
                    agents,
                    act,
                    onOpenAgent = { detail = "agent:" + it.id },
                    onOpenWorkspace = { detail = "ws:" + it.id },
                )
            }
        }
    }
}
