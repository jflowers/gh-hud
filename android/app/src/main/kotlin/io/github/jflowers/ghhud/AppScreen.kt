package io.github.jflowers.ghhud

import androidx.activity.compose.BackHandler
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.padding
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.List
import androidx.compose.material.icons.filled.Notifications
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
import io.github.jflowers.ghhud.core.ItemCommands
import io.github.jflowers.ghhud.core.ItemFilter
import io.github.jflowers.ghhud.core.ItemList
import io.github.jflowers.ghhud.core.PlanList
import kotlinx.coroutines.launch

/** The places in the bottom bar. More come with Plan, Agents and Fleet. */
enum class Place(val label: String, val icon: ImageVector) {
    INBOX("Inbox", Icons.Filled.Notifications),
    ITEMS("Items", Icons.AutoMirrored.Filled.List),
    PLAN("Plan", Icons.Filled.PlayArrow),
}

/** Everything the app's screens can ask for. */
interface AppActions : AttentionActions, ItemsControl, ItemActions, PlanActions

/**
 * The app: a bottom bar between the places, each a list; an item's row
 * opens its own screen, and Back returns to the list.
 */
@Composable
fun AppScreen(ui: Ui, items: ItemsView, act: AppActions) {
    var place by rememberSaveable { mutableStateOf(Place.INBOX) }
    var itemId by rememberSaveable { mutableStateOf<String?>(null) }
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
    BackHandler(enabled = itemId != null) { itemId = null }
    Scaffold(
        snackbarHost = { SnackbarHost(snackbar) },
        bottomBar = {
            NavigationBar {
                for (p in Place.entries) {
                    NavigationBarItem(
                        selected = p == place,
                        onClick = {
                            place = p
                            itemId = null
                        },
                        icon = {
                            val n = when (p) {
                                Place.INBOX -> ui.hub.attention.count
                                Place.PLAN -> PlanList.starting(ui.hub.snapshot)
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
            val id = itemId
            when {
                id != null -> {
                    val row = remember(ui.hub.snapshot, id) {
                        ItemList.rows(ui.hub.snapshot, ItemFilter.ALL).firstOrNull { it.id == id }
                    }
                    ItemScreen(row, ItemCommands.off(ui.hub), act, say, onBack = { itemId = null })
                }
                place == Place.INBOX -> AttentionScreen(ui, act)
                place == Place.ITEMS -> ItemsScreen(ui, items, act, onOpenItem = { itemId = it.id })
                place == Place.PLAN -> PlanScreen(ui, act, act, say, refresh = act::refresh)
            }
        }
    }
}
