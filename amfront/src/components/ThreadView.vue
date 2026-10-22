<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import type { Entry } from '../lib/atom'
import { fetchFeed } from '../lib/agg'
import { toItem, verifyItems, type Item } from '../lib/items'
import { threadDepths } from '../lib/thread'
import EntryCard from './EntryCard.vue'

const props = defineProps<{ url: string }>()
const emit = defineEmits<{ reply: [entry: Entry]; close: [] }>()

const items = ref<Item[]>([])
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

defineExpose({ reload })
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
      @reply="emit('reply', item.entry)"
    />
  </div>
</template>
