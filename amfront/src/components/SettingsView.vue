<script setup lang="ts">
import { reactive, ref } from 'vue'
import { createReader, deleteReader, login, logout, readers, type Reader, type ReaderCredential } from '../lib/agg'
import { isConfigured, isLoggedIn, normalizeBaseURL, settings } from '../lib/settings'

const emit = defineEmits<{ saved: [] }>()

// Edit a copy, so half-typed values don't take effect until saved.
const form = reactive({ feedServer: settings.feedServer, token: settings.token, timeline: settings.timeline })

function save() {
  settings.feedServer = normalizeBaseURL(form.feedServer)
  settings.token = form.token.trim()
  settings.timeline = form.timeline.trim()
  if (settings.source === 'custom' && !settings.timeline) settings.source = 'feed'
  emit('saved')
}

// --- aggregator ---

const agg = reactive({ url: '', invite: '' })
const busy = ref(false)
const aggError = ref('')

async function doLogin() {
  busy.value = true
  aggError.value = ''
  try {
    await login(agg.url, agg.invite.trim())
    agg.invite = ''
    loadReaders()
  } catch (e) {
    aggError.value = (e as Error).message
  } finally {
    busy.value = false
  }
}

// --- feed reader passwords ---

const readerList = ref<Reader[]>([])
const readerName = ref('')
const created = ref<ReaderCredential | null>(null)

async function loadReaders() {
  if (!isLoggedIn()) return
  try {
    readerList.value = await readers()
  } catch (e) {
    aggError.value = (e as Error).message
  }
}

async function addReader() {
  aggError.value = ''
  try {
    created.value = await createReader(readerName.value.trim())
    readerName.value = ''
    loadReaders()
  } catch (e) {
    aggError.value = (e as Error).message
  }
}

async function revoke(r: Reader) {
  if (!confirm(`Revoke the password for ${r.name}?`)) return
  try {
    await deleteReader(r.id)
    loadReaders()
  } catch (e) {
    aggError.value = (e as Error).message
  }
}

loadReaders()
</script>

<template>
  <form class="settings" @submit.prevent="save">
    <h2>Feed server</h2>
    <label>
      Feed server
      <input v-model="form.feedServer" type="url" placeholder="https://miki.one" required />
      <small>Your amfeed instance. Posts are published here.</small>
    </label>
    <label>
      Token
      <input v-model="form.token" type="password" placeholder="amf_…" required autocomplete="off" />
      <small>Create one with <code>am token create NAME</code>, or use the admin token from <code>amfeed init</code>.</small>
    </label>
    <label>
      Custom feed
      <input v-model="form.timeline" type="url" placeholder="(none)" />
      <small>Any Atom feed, selectable as the timeline.</small>
    </label>
    <button type="submit" class="primary">Save</button>
  </form>

  <section class="settings">
    <h2>Aggregator</h2>
    <template v-if="isLoggedIn()">
      <p>
        Logged in to <a :href="settings.aggregator" target="_blank" rel="noopener">{{ settings.aggregator }}</a> as
        <strong>{{ settings.aggName }}</strong>.
        <button class="link" @click="logout">Log out</button>
      </p>

      <h3>Feed reader passwords</h3>
      <p class="note">
        Read your timeline in any feed reader: create a password per reader, then subscribe to your timeline with HTTP
        Basic auth.
      </p>
      <div v-if="created" class="credential">
        <p>For <strong>{{ created.name }}</strong> (shown only once):</p>
        <p>
          Feed: <code>{{ created.timeline_url }}</code><br />
          User: <code>{{ created.username }}</code><br />
          Password: <code>{{ created.password }}</code>
        </p>
        <button class="link" @click="created = null">Done</button>
      </div>
      <form class="inline" @submit.prevent="addReader">
        <input v-model="readerName" placeholder="Reader name, e.g. NetNewsWire" required />
        <button type="submit">Create</button>
      </form>
      <ul class="plain">
        <li v-for="r in readerList" :key="r.id">
          {{ r.name }} <span class="muted">since {{ r.created }}</span>
          <button class="link danger" @click="revoke(r)">Revoke</button>
        </li>
      </ul>
    </template>
    <form v-else @submit.prevent="doLogin">
      <label>
        Aggregator
        <input v-model="agg.url" type="url" placeholder="https://capyclub.example" required />
      </label>
      <label>
        Invite code
        <input v-model="agg.invite" placeholder="inv_… (only to register)" autocomplete="off" />
      </label>
      <small v-if="!isConfigured()">Set up your feed server first: logging in publishes a challenge there.</small>
      <button type="submit" class="primary" :disabled="busy || !isConfigured()">
        {{ busy ? 'Logging in…' : 'Log in' }}
      </button>
    </form>
    <p v-if="aggError" class="error">{{ aggError }}</p>
    <p class="note">Settings are stored in this browser only.</p>
  </section>
</template>
