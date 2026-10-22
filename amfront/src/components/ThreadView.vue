<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import type { Entry } from '../lib/atom'
import { fetchFeed } from '../lib/agg'
import { toItem, verifyItems, type Item } from '../lib/items'
import { threadDepths } from '../lib/thread'
import ComposeBox from './ComposeBox.vue'
import EntryCard from './EntryCard.vue'

const props = defineProps<{ url: string }>()
const emit = defineEmits<{ close: [] }>()

const items = ref<Item[]>([])
/** The entry being replied to; the reply box opens under it. */
const replyTo = ref<Entry | null>(null)
const loading = ref(false)
const error = ref('')

// Indentation is capped, so deep threads stay readable on phones.
const depths = computed(() => threadDepths(items.value.map((i) => i.entry)).map((d) => Math.min(d, 6)))

async function reload() {
  loading.value = true
  error.value = ''
  try {
    const feed = await fetchFeed(props.url)
    items.value = feed.entries.map(toItem)
    verifyItems(items)
  } catch (e) {
    error.value = (e as Error).message
  } finally {
    loading.value = false
  }
}

function posted() {
  replyTo.value = null
  reload()
}

watch(() => props.url, reload)
onMounted(reload)
</script>

<template>
  <div class="timeline-header">
    <h2>Thread</h2>
    <button class="link" :disabled="loading" @click="reload">Refresh</button>
    <button class="link" @click="emit('close')">Back</button>
  </div>
  <p v-if="error" class="error">{{ error }}</p>
  <div v-for="(item, i) in items" :key="item.entry.id" class="thread-item" :style="{ marginLeft: depths[i] + 'rem' }">
    <EntryCard
      :entry="item.entry"
      :verified="item.verified"
      :own-seq="null"
      @reply="replyTo = item.entry"
    />
    <ComposeBox
      v-if="replyTo?.id === item.entry.id"
      :reply-to="replyTo"
      @posted="posted"
      @cancel-reply="replyTo = null"
    />
  </div>
</template>
