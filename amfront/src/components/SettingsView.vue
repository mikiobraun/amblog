<script setup lang="ts">
import { reactive } from 'vue'
import { normalizeBaseURL, settings } from '../lib/settings'

const emit = defineEmits<{ saved: [] }>()

// Edit a copy, so half-typed values don't take effect until saved.
const form = reactive({ ...settings })

function save() {
  settings.feedServer = normalizeBaseURL(form.feedServer)
  settings.token = form.token.trim()
  settings.timeline = form.timeline.trim()
  emit('saved')
}
</script>

<template>
  <form class="settings" @submit.prevent="save">
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
      Timeline feed
      <input v-model="form.timeline" type="url" placeholder="(your own feed)" />
      <small>Any Atom feed. Empty shows your own feed; later, an aggregator timeline goes here.</small>
    </label>
    <p class="note">Settings are stored in this browser only.</p>
    <button type="submit" class="primary">Save</button>
  </form>
</template>
