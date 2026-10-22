<script setup lang="ts">
import { onMounted, ref, watch } from 'vue'
import { linkHref, type Entry } from '../lib/atom'
import { fetchFeed } from '../lib/agg'
import { deleteEntry } from '../lib/api'
import { toItem, verifyItems, type Item } from '../lib/items'
import { go, route } from '../lib/route'
import { isLoggedIn, settings, timelineURL } from '../lib/settings'
import ComposeBox from './ComposeBox.vue'
import EntryCard from './EntryCard.vue'
import ThreadView from './ThreadView.vue'

const items = ref<Item[]>([])
const title = ref('')
const next = ref('')
const loading = ref(false)
const error = ref('')
const replyTo = ref<Entry | null>(null)
const threadView = ref<InstanceType<typeof ThreadView> | null>(null)

async function load(url: string, append: boolean) {
  loading.value = true
  error.value = ''
  try {
    const feed = await fetchFeed(url)
    const start = append ? items.value.length : 0
    const loaded = feed.entries.map(toItem)
    items.value = append ? [...items.value, ...loaded] : loaded
    if (!append) title.value = feed.title
    next.value = linkHref(feed.links, 'next')
    verifyItems(items, start)
  } catch (e) {
    error.value = (e as Error).message
  } finally {
    loading.value = false
  }
}

function reload() {
  const url = timelineURL()
  if (!url) {
    items.value = []
    error.value = 'No timeline configured. Set a feed server (and an aggregator) in Settings.'
    return
  }
  load(url, false)
}

function posted() {
  replyTo.value = null
  if (route.thread) threadView.value?.reload()
  else reload()
}

async function remove(item: Item) {
  if (item.ownSeq === null) return
  if (!confirm('Delete this post? It stays in your feed as a tombstone.')) return
  try {
    await deleteEntry(item.ownSeq)
    reload()
  } catch (e) {
    error.value = (e as Error).message
  }
}

watch(() => settings.source, reload)
onMounted(reload)
</script>

<template>
  <ComposeBox :reply-to="replyTo" @posted="posted" @cancel-reply="replyTo = null" />
  <ThreadView
    v-if="route.thread"
    ref="threadView"
    :url="route.thread"
    @reply="replyTo = $event"
    @close="go('timeline')"
  />
  <template v-else>
    <div class="timeline-header">
      <h2>{{ title || 'Timeline' }}</h2>
      <select v-model="settings.source" aria-label="Timeline">
        <option value="feed">My feed</option>
        <option v-if="isLoggedIn()" value="home">Home ({{ settings.aggName }})</option>
        <option v-if="isLoggedIn()" value="island">Island</option>
        <option v-if="settings.timeline" value="custom">Custom feed</option>
      </select>
      <button class="link" :disabled="loading" @click="reload">Refresh</button>
    </div>
    <p v-if="error" class="error">{{ error }}</p>
    <EntryCard
      v-for="item in items"
      :key="item.entry.id"
      :entry="item.entry"
      :verified="item.verified"
      :own-seq="item.ownSeq"
      :thread-url="item.threadUrl"
      @reply="replyTo = item.entry"
      @thread="go('timeline', item.threadUrl)"
      @delete="remove(item)"
    />
    <p v-if="!loading && !error && items.length === 0" class="muted">Nothing here yet.</p>
    <button v-if="next" class="more" :disabled="loading" @click="load(next, true)">
      {{ loading ? 'Loading…' : 'Load more' }}
    </button>
  </template>
</template>
