<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { fetchAtom, linkHref, type Entry, type Feed } from '../lib/atom'
import { KeyCache, UnverifiableError } from '../lib/sign'
import { deleteEntry, ownPostNumber } from '../lib/api'
import { timelineURL } from '../lib/settings'
import ComposeBox from './ComposeBox.vue'
import EntryCard, { type VerifyStatus } from './EntryCard.vue'

interface Item {
  entry: Entry
  verified: VerifyStatus
  ownSeq: number | null
}

const items = ref<Item[]>([])
const title = ref('')
const next = ref('')
const loading = ref(false)
const error = ref('')
const replyTo = ref<Entry | null>(null)

const keys = new KeyCache()

function toItems(feed: Feed): Item[] {
  return feed.entries.map((entry) => ({
    entry,
    verified: { state: 'pending' },
    ownSeq: ownPostNumber(linkHref(entry.links, 'alternate')),
  }))
}

// Verifies in the background; each item's badge updates when done. Mutates
// through the reactive array so Vue sees the change.
function verifyAll(start: number) {
  items.value.slice(start).forEach((item, i) => {
    keys.verify(item.entry).then(
      () => (items.value[start + i].verified = { state: 'ok' }),
      (e: Error) =>
        (items.value[start + i].verified =
          e instanceof UnverifiableError
            ? { state: 'unverifiable', reason: e.message }
            : { state: 'failed', reason: e.message }),
    )
  })
}

async function load(url: string, append: boolean) {
  loading.value = true
  error.value = ''
  try {
    const feed = await fetchAtom(url)
    const start = append ? items.value.length : 0
    items.value = append ? [...items.value, ...toItems(feed)] : toItems(feed)
    if (!append) title.value = feed.title
    next.value = linkHref(feed.links, 'next')
    verifyAll(start)
  } catch (e) {
    error.value = (e as Error).message
  } finally {
    loading.value = false
  }
}

function reload() {
  const url = timelineURL()
  if (!url) {
    error.value = 'No timeline configured. Set a feed server in Settings.'
    return
  }
  load(url, false)
}

function posted() {
  replyTo.value = null
  reload()
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

onMounted(reload)
</script>

<template>
  <ComposeBox :reply-to="replyTo" @posted="posted" @cancel-reply="replyTo = null" />
  <div class="timeline-header">
    <h2>{{ title || 'Timeline' }}</h2>
    <button class="link" :disabled="loading" @click="reload">Refresh</button>
  </div>
  <p v-if="error" class="error">{{ error }}</p>
  <EntryCard
    v-for="item in items"
    :key="item.entry.id"
    :entry="item.entry"
    :verified="item.verified"
    :own-seq="item.ownSeq"
    @reply="replyTo = item.entry"
    @delete="remove(item)"
  />
  <p v-if="!loading && !error && items.length === 0" class="muted">Nothing here yet.</p>
  <button v-if="next" class="more" :disabled="loading" @click="load(next, true)">
    {{ loading ? 'Loading…' : 'Load more' }}
  </button>
</template>
