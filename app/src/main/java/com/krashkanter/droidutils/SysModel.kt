package com.krashkanter.droidutils

import android.app.Application
import androidx.lifecycle.AndroidViewModel
import androidx.lifecycle.viewModelScope
import java.io.File
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.RequestBody.Companion.toRequestBody
import org.json.JSONArray
import org.json.JSONObject

fun JSONArray.rows(): List<JSONObject> = (0 until length()).map { getJSONObject(it) }
fun JSONObject.payload(): JSONObject = JSONObject().put("id", optString("id")).put("name", optString("name")).put("body", optString("body")).put("enabled", optBoolean("enabled")).put("order", optInt("order", 100)).put("timeout", optInt("timeout", 60))
class LocalApi(application: Application) {
    private val file = File(application.filesDir, "token")
    private val client = OkHttpClient()
    fun pair(token: String) { require(token.matches(Regex("[a-f0-9]{64}"))) { "Invalid token" }; file.writeText(token) }
    suspend fun request(method: String, path: String, body: JSONObject? = null): String = withContext(Dispatchers.IO) {
        val token = if (file.exists()) file.readText().trim() else ""
        check(token.isNotEmpty()) { "Pair DroidUtils with the local service" }
        val request = Request.Builder().url("http://127.0.0.1:9474/v1/$path").header("Authorization", "Bearer $token")
            .method(method, if (method in listOf("POST", "PUT")) (body ?: JSONObject()).toString().toRequestBody("application/json".toMediaType()) else null).build()
        client.newCall(request).execute().use { response ->
            val text = response.body?.string().orEmpty()
            check(response.isSuccessful) { runCatching { JSONObject(text).getString("error") }.getOrDefault("HTTP ${response.code}") }
            text
        }
    }
}
data class SysState(
    val status: JSONObject? = null,
    val scripts: List<JSONObject> = emptyList(),
    val settings: List<JSONObject> = emptyList(),
    val services: List<JSONObject> = emptyList(),
    val updates: JSONObject? = null,
    val updatesLoading: Boolean = false,
    val namespace: String = "global",
    val connected: Boolean = false,
    val refreshing: Boolean = false,
    val busy: Boolean = false,
    val error: String? = null,
    val notice: String? = null,
)
class SysModel(application: Application): AndroidViewModel(application) {
    val api = LocalApi(application)
    private val mutable = MutableStateFlow(SysState())
    val state = mutable.asStateFlow()
    private var loading = false
    fun clearMessage() { mutable.value = mutable.value.copy(error = null, notice = null) }
    fun message(value: String) { mutable.value = mutable.value.copy(notice = value) }
    fun namespace(value: String) { mutable.value = mutable.value.copy(namespace = value); refresh(true) }
    fun refresh(manual: Boolean = false) {
        if (loading) return
        loading = true
        if (manual) mutable.value = mutable.value.copy(refreshing = true)
        viewModelScope.launch {
            try {
                val ns = mutable.value.namespace
                val status = JSONObject(api.request("GET", "status"))
                val scripts = JSONArray(api.request("GET", "scripts")).rows()
                val settings = JSONArray(api.request("GET", "settings?namespace=$ns")).rows()
                val services = JSONArray(api.request("GET", "services")).rows()
                mutable.value = mutable.value.copy(status = status, scripts = scripts, settings = settings, services = services, connected = true, refreshing = false)
            } catch (e: Exception) {
                mutable.value = mutable.value.copy(connected = false, refreshing = false, error = if (manual || mutable.value.status == null) e.message else mutable.value.error)
            } finally { loading = false }
        }
    }
    private var loadingUpdates = false
    fun refreshUpdates(manual: Boolean = false) {
        if (loadingUpdates) return
        loadingUpdates = true
        if (manual) mutable.value = mutable.value.copy(updatesLoading = true)
        viewModelScope.launch {
            try { mutable.value = mutable.value.copy(updates = JSONObject(api.request("GET", "updates")), updatesLoading = false) }
            catch (e: Exception) { mutable.value = mutable.value.copy(updatesLoading = false, error = if (manual || mutable.value.updates == null) e.message else mutable.value.error) }
            finally { loadingUpdates = false }
        }
    }
    fun updateAction(path: String, body: JSONObject? = null, done: () -> Unit = {}) {
        if (mutable.value.busy) return
        mutable.value = mutable.value.copy(busy = true)
        viewModelScope.launch {
            try { api.request("POST", path, body); done(); refreshUpdates(true) }
            catch (e: Exception) { mutable.value = mutable.value.copy(error = e.message) }
            finally { mutable.value = mutable.value.copy(busy = false) }
        }
    }
    fun action(method: String, path: String, body: JSONObject? = null, done: () -> Unit = {}) {
        if (mutable.value.busy) return
        mutable.value = mutable.value.copy(busy = true)
        viewModelScope.launch {
            try { api.request(method, path, body); done(); refresh(true) }
            catch (e: Exception) { mutable.value = mutable.value.copy(error = e.message) }
            finally { mutable.value = mutable.value.copy(busy = false) }
        }
    }
}