@file:OptIn(ExperimentalMaterial3Api::class, ExperimentalMaterial3ExpressiveApi::class)
package com.krashkanter.droidutils

import android.os.Bundle
import androidx.activity.ComponentActivity
import androidx.activity.compose.BackHandler
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.compose.setContent
import androidx.activity.enableEdgeToEdge
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.clickable
import androidx.compose.foundation.isSystemInDarkTheme
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.foundation.text.selection.SelectionContainer
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.rounded.ArrowBack
import androidx.compose.material.icons.automirrored.rounded.Article
import androidx.compose.material.icons.rounded.*
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.text.input.PasswordVisualTransformation
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.compose.LocalLifecycleOwner
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import androidx.lifecycle.repeatOnLifecycle
import androidx.lifecycle.viewmodel.compose.viewModel
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import org.json.JSONObject

class MainActivity: ComponentActivity() {
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        enableEdgeToEdge()
        setContent {
            val context = LocalContext.current
            MaterialExpressiveTheme(colorScheme = if (isSystemInDarkTheme()) dynamicDarkColorScheme(context) else dynamicLightColorScheme(context)) { SysApp() }
        }
    }
}
@Composable fun Action(label: String, icon: ImageVector, enabled: Boolean = true, click: () -> Unit) {
    TooltipBox(positionProvider = TooltipDefaults.rememberPlainTooltipPositionProvider(), tooltip = { PlainTooltip { Text(label) } }, state = rememberTooltipState()) {
        IconButton(onClick = click, enabled = enabled) { Icon(icon, label) }
    }
}
@Composable fun SysApp(model: SysModel = viewModel()) {
    val state by model.state.collectAsStateWithLifecycle()
    val lifecycle = LocalLifecycleOwner.current.lifecycle
    val context = LocalContext.current
    val scope = rememberCoroutineScope()
    var section by rememberSaveable { mutableIntStateOf(0) }
    var query by rememberSaveable { mutableStateOf("") }
    var editor by remember { mutableStateOf<JSONObject?>(null) }
    var logs by remember { mutableStateOf<JSONObject?>(null) }
    var deleting by remember { mutableStateOf<JSONObject?>(null) }
    var setting by remember { mutableStateOf<JSONObject?>(null) }
    var service by remember { mutableStateOf<Pair<String, String>?>(null) }
    var updating by remember { mutableStateOf<JSONObject?>(null) }
    var pairing by rememberSaveable { mutableStateOf(false) }
    val snackbar = remember { SnackbarHostState() }
    val importer = rememberLauncherForActivityResult(ActivityResultContracts.GetContent()) { uri ->
        if (uri != null) scope.launch {
            runCatching { withContext(Dispatchers.IO) {
                context.contentResolver.openInputStream(uri)?.use { input ->
                    val bytes = ByteArray(128 * 1024 + 1)
                    var count = 0
                    while (count < bytes.size) {
                        val size = input.read(bytes, count, bytes.size - count)
                        if (size < 0) break
                        count += size
                    }
                    check(count <= 128 * 1024) { "Script exceeds 128 KiB" }
                    String(bytes, 0, count, Charsets.UTF_8)
                } ?: error("Cannot read script")
            } }.onSuccess { editor = JSONObject().put("name", "Imported script").put("body", it).put("enabled", false).put("timeout", 60).put("order", 100) }
                .onFailure { model.message(it.message ?: "Import failed") }
        }
    }
    LaunchedEffect(lifecycle) { lifecycle.repeatOnLifecycle(Lifecycle.State.STARTED) { model.refresh(true); while (true) { delay(5000); model.refresh() } } }
    LaunchedEffect(lifecycle, section) { if (section == 3) lifecycle.repeatOnLifecycle(Lifecycle.State.STARTED) {
        model.refreshUpdates(true)
        while (true) { delay(if (model.state.value.updates?.optBoolean("running") == true) 2000 else 10000); model.refreshUpdates() }
    } }
    LaunchedEffect(state.error, state.notice) { (state.error ?: state.notice)?.let { snackbar.showSnackbar(it); model.clearMessage() } }
    editor?.let { draft -> ScriptEditor(draft, state.busy, { editor = null }) { payload ->
        val id = draft.optString("id")
        model.action(if (id.isEmpty()) "POST" else "PUT", if (id.isEmpty()) "scripts" else "scripts/$id", payload) { editor = null }
    }; return }
    Scaffold(
        topBar = { TopAppBar(title = { Text("DroidUtils", fontWeight = FontWeight.SemiBold) }, actions = {
            Action("Refresh", Icons.Rounded.Refresh, !state.refreshing) { model.refresh(true); if (section == 3) model.refreshUpdates(true) }
            Action("Connection", Icons.Rounded.Tune) { pairing = true }
        }) },
        bottomBar = { NavigationBar {
            listOf("Scripts" to Icons.Rounded.Terminal, "Settings" to Icons.Rounded.Settings, "Services" to Icons.Rounded.MiscellaneousServices, "Updates" to Icons.Rounded.SystemUpdate).forEachIndexed { index, item ->
                NavigationBarItem(selected = section == index, onClick = { section = index; query = "" }, icon = { Icon(item.second, item.first) }, label = { Text(item.first) }, alwaysShowLabel = false)
            }
        } },
        snackbarHost = { SnackbarHost(snackbar) },
        floatingActionButton = { if (section < 2) FloatingActionButton(onClick = {
            if (section == 0) editor = JSONObject().put("name", "").put("body", "#!/system/bin/sh\n").put("enabled", false).put("timeout", 60).put("order", 100)
            else setting = JSONObject().put("key", "").put("value", "")
        }) { Icon(Icons.Rounded.Add, if (section == 0) "Create script" else "Create setting") } }
    ) { padding ->
        Column(Modifier.fillMaxSize().padding(padding)) {
            Row(Modifier.fillMaxWidth().padding(horizontal = 24.dp, vertical = 8.dp), verticalAlignment = Alignment.CenterVertically) {
                Icon(if (state.connected) Icons.Rounded.CheckCircle else Icons.Rounded.ErrorOutline, null, tint = if (state.connected) MaterialTheme.colorScheme.primary else MaterialTheme.colorScheme.error, modifier = Modifier.size(18.dp))
                Spacer(Modifier.width(8.dp))
                Text(if (state.connected) "uid ${state.status?.optInt("uid")} · ${state.scripts.size} scripts" else "Service unavailable", style = MaterialTheme.typography.labelLarge)
                Spacer(Modifier.weight(1f))
                if (section == 0) Action("Import script", Icons.Rounded.FileOpen) { importer.launch("*/*") }
            }
            if (state.refreshing || (section == 3 && state.updatesLoading)) LinearProgressIndicator(Modifier.fillMaxWidth())
            if (section == 1) SingleChoiceSegmentedButtonRow(Modifier.fillMaxWidth().padding(horizontal = 20.dp, vertical = 8.dp)) {
                listOf("global", "secure", "system").forEachIndexed { i, ns -> SegmentedButton(selected = state.namespace == ns, onClick = { model.namespace(ns) }, shape = SegmentedButtonDefaults.itemShape(i, 3)) { Text(ns.replaceFirstChar { it.uppercase() }) } }
            }
            OutlinedTextField(value = query, onValueChange = { query = it }, placeholder = { Text("Search") }, leadingIcon = { Icon(Icons.Rounded.Search, null) }, singleLine = true, modifier = Modifier.fillMaxWidth().padding(horizontal = 20.dp, vertical = 8.dp), shape = MaterialTheme.shapes.extraLarge)
            when (section) {
                0 -> LazyColumn(contentPadding = PaddingValues(start = 20.dp, end = 20.dp, bottom = 96.dp), verticalArrangement = Arrangement.spacedBy(10.dp)) {
                    val rows = state.scripts.filter { it.optString("name").contains(query, true) || it.optString("body").contains(query, true) }
                    if (rows.isEmpty()) item { Text("No scripts", Modifier.padding(16.dp), color = MaterialTheme.colorScheme.onSurfaceVariant) }
                    items(rows, key = { it.getString("id") }) { script ->
                        val result = script.optJSONObject("result") ?: JSONObject()
                        val running = result.optBoolean("running")
                        ElevatedCard(Modifier.fillMaxWidth()) {
                            Row(Modifier.fillMaxWidth().clickable { editor = script }.padding(start = 16.dp, end = 12.dp, top = 12.dp), verticalAlignment = Alignment.CenterVertically) {
                                Column(Modifier.weight(1f)) {
                                    Text(script.getString("name"), style = MaterialTheme.typography.titleMedium, maxLines = 1, overflow = TextOverflow.Ellipsis)
                                    Text("${script.optInt("order")} · ${script.optInt("timeout")}s · " + when { running -> "Running"; result.optString("started").isEmpty() -> "Not run"; result.optInt("exit") == 0 -> "Exit 0"; else -> "Exit ${result.optInt("exit")}" }, style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
                                }
                                Switch(checked = script.optBoolean("enabled"), enabled = !state.busy && !running, onCheckedChange = { enabled -> model.action("PUT", "scripts/${script.getString("id")}", script.payload().put("enabled", enabled)) })
                            }
                            Row(Modifier.fillMaxWidth().padding(horizontal = 6.dp), horizontalArrangement = Arrangement.End) {
                                Action("Edit", Icons.Rounded.Edit, !running) { editor = script }
                                Action("Logs", Icons.AutoMirrored.Rounded.Article) { logs = script }
                                Action(if (running) "Stop" else "Run", if (running) Icons.Rounded.Stop else Icons.Rounded.PlayArrow, !state.busy) { model.action("POST", "scripts/${script.getString("id")}/${if (running) "stop" else "run"}") }
                                Action("Delete", Icons.Rounded.DeleteOutline, !running && !state.busy) { deleting = script }
                            }
                        }
                    }
                }
                1 -> LazyColumn(contentPadding = PaddingValues(start = 20.dp, end = 20.dp, bottom = 96.dp)) {
                    items(state.settings.filter { it.getString("key").contains(query, true) || it.getString("value").contains(query, true) }, key = { it.getString("key") }) { row ->
                        ListItem(headlineContent = { Text(row.getString("key"), fontFamily = FontFamily.Monospace, style = MaterialTheme.typography.bodyMedium) }, supportingContent = { Text(row.getString("value"), maxLines = 2, overflow = TextOverflow.Ellipsis) }, modifier = Modifier.clickable { setting = row })
                        HorizontalDivider(color = MaterialTheme.colorScheme.outlineVariant.copy(alpha = 0.4f))
                    }
                }
                2 -> LazyColumn(contentPadding = PaddingValues(start = 20.dp, end = 20.dp, bottom = 24.dp)) {
                    items(state.services.filter { it.getString("name").contains(query, true) }, key = { it.getString("name") }) { row ->
                        val running = row.getString("state") == "running"
                        val name = row.getString("name")
                        ListItem(headlineContent = { Text(name, fontFamily = FontFamily.Monospace, style = MaterialTheme.typography.bodyMedium) }, supportingContent = { Text(row.getString("state")) }, trailingContent = { Action(if (running) "Stop" else "Start", if (running) Icons.Rounded.Stop else Icons.Rounded.PlayArrow, !state.busy && name !in listOf("sysutils", "adbd")) { service = name to if (running) "stop" else "start" } })
                    }
                }
                3 -> UpdatesList(state, query, { model.updateAction("updates/check") }) { updating = it }
            }
        }
    }
    deleting?.let { script -> AlertDialog(onDismissRequest = { deleting = null }, title = { Text("Delete ${script.getString("name")}?") }, confirmButton = { Action("Delete", Icons.Rounded.Delete) { model.action("DELETE", "scripts/${script.getString("id")}") { deleting = null } } }, dismissButton = { Action("Cancel", Icons.Rounded.Close) { deleting = null } }) }
    service?.let { (name, action) -> AlertDialog(onDismissRequest = { service = null }, title = { Text("${action.replaceFirstChar { it.uppercase() }} $name?") }, confirmButton = { Action("Confirm", Icons.Rounded.Check) { model.action("POST", "services/$name/$action") { service = null } } }, dismissButton = { Action("Cancel", Icons.Rounded.Close) { service = null } }) }
    setting?.let { row -> SettingEditor(state.namespace, row, state.busy, { setting = null }) { key, value, delete -> model.action("PUT", "settings", JSONObject().put("namespace", state.namespace).put("key", key).put("value", value).put("delete", delete)) { setting = null } } }
    logs?.let { LogViewer(model, it, { logs = null }) }
    updating?.let { item -> UpdateConfirm(item, state.busy || state.updates?.optBoolean("running") == true, { updating = null }) { model.updateAction("updates/apply", JSONObject().put("id", item.getString("id")).put("available", item.getString("available"))) { updating = null } } }
    if (pairing) {
        var token by remember { mutableStateOf("") }
        AlertDialog(onDismissRequest = { pairing = false }, title = { Text("Local service") }, text = { Column(verticalArrangement = Arrangement.spacedBy(12.dp)) {
            Text("127.0.0.1:9474", fontFamily = FontFamily.Monospace)
            OutlinedTextField(token, { token = it }, label = { Text("Pairing token") }, visualTransformation = PasswordVisualTransformation(), singleLine = true)
        } }, confirmButton = { Action("Pair", Icons.Rounded.Link, token.length == 64) { runCatching { model.api.pair(token); pairing = false; model.refresh(true) }.onFailure { model.message(it.message ?: "Pairing failed") } } }, dismissButton = { Action("Close", Icons.Rounded.Close) { pairing = false } })
    }
}

@Composable fun ScriptEditor(script: JSONObject, busy: Boolean, close: () -> Unit, save: (JSONObject) -> Unit) {
    var name by rememberSaveable { mutableStateOf(script.optString("name")) }
    var body by rememberSaveable { mutableStateOf(script.optString("body")) }
    var enabled by rememberSaveable { mutableStateOf(script.optBoolean("enabled")) }
    var order by rememberSaveable { mutableStateOf(script.optInt("order", 100).toString()) }
    var timeout by rememberSaveable { mutableStateOf(script.optInt("timeout", 60).toString()) }
    var discard by remember { mutableStateOf(false) }
    val dirty = name != script.optString("name") || body != script.optString("body") || enabled != script.optBoolean("enabled") || order != script.optInt("order", 100).toString() || timeout != script.optInt("timeout", 60).toString()
    val leave = { if (dirty) discard = true else close() }
    BackHandler { leave() }
    Scaffold(topBar = { TopAppBar(title = { Text(if (script.optString("id").isEmpty()) "New script" else "Edit script") }, navigationIcon = { Action("Back", Icons.AutoMirrored.Rounded.ArrowBack, !busy) { leave() } }, actions = { Action("Save", Icons.Rounded.Save, !busy && name.isNotBlank() && body.isNotBlank() && order.toIntOrNull() in 0..9999 && timeout.toIntOrNull() in 1..3600) { save(JSONObject().put("name", name).put("body", body).put("enabled", enabled).put("order", order.toInt()).put("timeout", timeout.toInt())) } }) }) { padding ->
        Column(Modifier.fillMaxSize().padding(padding).imePadding().verticalScroll(rememberScrollState()).padding(20.dp), verticalArrangement = Arrangement.spacedBy(12.dp)) {
            OutlinedTextField(name, { name = it }, label = { Text("Name") }, singleLine = true, modifier = Modifier.fillMaxWidth())
            Row(verticalAlignment = Alignment.CenterVertically) { Text("Run at startup", Modifier.weight(1f)); Switch(enabled, { enabled = it }) }
            Row(horizontalArrangement = Arrangement.spacedBy(12.dp)) {
                OutlinedTextField(order, { order = it.filter(Char::isDigit) }, label = { Text("Order") }, keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Number), singleLine = true, modifier = Modifier.weight(1f))
                OutlinedTextField(timeout, { timeout = it.filter(Char::isDigit) }, label = { Text("Timeout (s)") }, keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Number), singleLine = true, modifier = Modifier.weight(1f))
            }
            OutlinedTextField(body, { body = it }, label = { Text("Shell script") }, modifier = Modifier.fillMaxWidth().heightIn(min = 360.dp), textStyle = MaterialTheme.typography.bodyMedium.copy(fontFamily = FontFamily.Monospace))
        }
    }
    if (discard) AlertDialog(onDismissRequest = { discard = false }, title = { Text("Discard changes?") }, confirmButton = { Action("Discard", Icons.Rounded.DeleteOutline) { close() } }, dismissButton = { Action("Keep editing", Icons.Rounded.Close) { discard = false } })
}
@Composable fun SettingEditor(namespace: String, row: JSONObject, busy: Boolean, close: () -> Unit, save: (String, String, Boolean) -> Unit) {
    var key by rememberSaveable { mutableStateOf(row.getString("key")) }
    var value by rememberSaveable { mutableStateOf(row.getString("value")) }
    var deleting by remember { mutableStateOf(false) }
    AlertDialog(onDismissRequest = close, title = { Text(namespace.replaceFirstChar { it.uppercase() }) }, text = { Column(verticalArrangement = Arrangement.spacedBy(12.dp)) { OutlinedTextField(key, { key = it }, label = { Text("Key") }, singleLine = true, readOnly = row.getString("key").isNotEmpty()); OutlinedTextField(value, { value = it }, label = { Text("Value") }) } }, confirmButton = { Action("Save", Icons.Rounded.Save, !busy && key.isNotBlank()) { save(key, value, false) } }, dismissButton = { Row { if (row.getString("key").isNotEmpty()) Action("Delete setting", Icons.Rounded.DeleteOutline, !busy) { deleting = true }; Action("Cancel", Icons.Rounded.Close) { close() } } })
    if (deleting) AlertDialog(onDismissRequest = { deleting = false }, title = { Text("Delete $key?") }, confirmButton = { Action("Delete", Icons.Rounded.Delete) { save(key, value, true) } }, dismissButton = { Action("Cancel", Icons.Rounded.Close) { deleting = false } })
}
@Composable fun LogViewer(model: SysModel, script: JSONObject, close: () -> Unit) {
    var text by remember { mutableStateOf("") }
    var status by remember { mutableStateOf("") }
    LaunchedEffect(script.getString("id")) { while (true) {
        runCatching { JSONObject(model.api.request("GET", "scripts/${script.getString("id")}/logs")) }.onSuccess {
            text = it.optString("text"); val result = it.optJSONObject("result") ?: JSONObject(); status = if (result.optBoolean("running")) "Running" else if (result.optString("started").isEmpty()) "Not run" else "Exit ${result.optInt("exit")} ${result.optString("error")}".trim()
        }.onFailure { status = it.message ?: "Unavailable" }; delay(1000)
    } }
    AlertDialog(onDismissRequest = close, title = { Column { Text(script.getString("name")); Text(status, style = MaterialTheme.typography.bodySmall) } }, text = { SelectionContainer { Text(text.ifEmpty { "No output" }, fontFamily = FontFamily.Monospace, style = MaterialTheme.typography.bodySmall, modifier = Modifier.fillMaxWidth().heightIn(max = 420.dp).verticalScroll(rememberScrollState())) } }, confirmButton = { Action("Close", Icons.Rounded.Close) { close() } })
}

fun restartImpact(item: JSONObject): String {
    val key = (item.optString("id") + " " + item.optString("name")).lowercase()
    return when {
        "apk" in key || ("droidutils" in key && "service" !in key) -> "DroidUtils will be reinstalled and this app will close. Reopen it once the update finishes."
        "sysutils" in key || "service" in key -> "The local root service restarts. The app disconnects for a few seconds and running scripts are stopped."
        "debian" in key || "apt" in key || "chroot" in key -> "Packages inside the Debian chroot are upgraded. Daemons running in the chroot may restart; Android itself is not rebooted."
        "magisk" in key || "kernel" in key || "lineage" in key || "rom" in key || "system" in key -> "A device reboot may be required before the update takes effect."
        else -> "The affected component may restart while the update is applied."
    }
}
@Composable fun UpdatesList(state: SysState, query: String, check: () -> Unit, apply: (JSONObject) -> Unit) {
    val updates = state.updates
    val running = updates?.optBoolean("running") == true
    val items = updates?.optJSONArray("items")?.rows().orEmpty().filter { it.optString("name").contains(query, true) || it.optString("id").contains(query, true) }
    LazyColumn(contentPadding = PaddingValues(start = 20.dp, end = 20.dp, bottom = 24.dp), verticalArrangement = Arrangement.spacedBy(10.dp)) {
        item {
            Row(Modifier.fillMaxWidth().padding(vertical = 4.dp), verticalAlignment = Alignment.CenterVertically) {
                Column(Modifier.weight(1f)) {
                    Text(when { updates == null -> "Not loaded"; running -> updates.optString("operation").ifEmpty { "Working" }.replaceFirstChar { it.uppercase() } + "…"; else -> updates.optString("operation").ifEmpty { "${items.count { it.optBoolean("installable") }} ready to install" } }, style = MaterialTheme.typography.titleSmall)
                    updates?.optString("error")?.takeIf { it.isNotEmpty() && !running }?.let { Text(it, style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.error) }
                }
                if (running) CircularProgressIndicator(Modifier.size(20.dp), strokeWidth = 2.dp)
                FilledTonalButton(onClick = check, enabled = !state.busy && !running && state.connected, modifier = Modifier.padding(start = 12.dp)) { Icon(Icons.Rounded.Sync, null, Modifier.size(18.dp)); Spacer(Modifier.width(8.dp)); Text("Check") }
            }
        }
        if (updates != null && items.isEmpty()) item { Text(if (query.isEmpty()) "No components reported" else "No matches", Modifier.padding(16.dp), color = MaterialTheme.colorScheme.onSurfaceVariant) }
        items(items, key = { it.getString("id") }) { item ->
            val current = item.optString("current").ifEmpty { "unknown" }
            val available = item.optString("available")
            ElevatedCard(Modifier.fillMaxWidth()) {
                Row(Modifier.fillMaxWidth().padding(start = 16.dp, end = 8.dp, top = 12.dp, bottom = 12.dp), verticalAlignment = Alignment.CenterVertically) {
                    Column(Modifier.weight(1f), verticalArrangement = Arrangement.spacedBy(2.dp)) {
                        Text(item.optString("name").ifEmpty { item.getString("id") }, style = MaterialTheme.typography.titleMedium, maxLines = 1, overflow = TextOverflow.Ellipsis)
                        Text(if (available.isNotEmpty() && available != current) "$current → $available" else current, fontFamily = FontFamily.Monospace, style = MaterialTheme.typography.bodySmall)
                        Text(listOf(item.optString("status"), item.optString("detail")).filter { it.isNotEmpty() }.joinToString(" · "), style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant, maxLines = 12, overflow = TextOverflow.Ellipsis)
                    }
                    if (item.optBoolean("installable")) Action("Update ${item.optString("name")}", Icons.Rounded.Download, !state.busy && !running) { apply(item) }
                    else if (item.optString("status") == "current") Icon(Icons.Rounded.CheckCircle, null, Modifier.padding(12.dp).size(20.dp), tint = MaterialTheme.colorScheme.outline)
                }
            }
        }
    }
}
@Composable fun UpdateConfirm(item: JSONObject, blocked: Boolean, close: () -> Unit, confirm: () -> Unit) {
    val name = item.optString("name").ifEmpty { item.getString("id") }
    AlertDialog(onDismissRequest = close, icon = { Icon(Icons.Rounded.SystemUpdate, null) }, title = { Text("Update $name?") }, text = { Column(verticalArrangement = Arrangement.spacedBy(10.dp)) {
        Text("${item.optString("current").ifEmpty { "unknown" }} → ${item.optString("available").ifEmpty { "latest" }}", fontFamily = FontFamily.Monospace)
        item.optString("detail").takeIf { it.isNotEmpty() }?.let { Text(it, style = MaterialTheme.typography.bodyMedium) }
        Row(verticalAlignment = Alignment.Top) {
            Icon(Icons.Rounded.RestartAlt, null, Modifier.size(18.dp), tint = MaterialTheme.colorScheme.tertiary); Spacer(Modifier.width(8.dp))
            Text(restartImpact(item), style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
        }
    } }, confirmButton = { TextButton(onClick = confirm, enabled = !blocked) { Text("Update") } }, dismissButton = { TextButton(onClick = close) { Text("Cancel") } })
}
