<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import DOMPurify from 'dompurify'
import { linkHref, type Entry } from '../lib/atom'
import type { VerifyStatus } from '../lib/sign'

const props = defineProps<{
  entry: Entry
  verified: VerifyStatus
  /** Post number on our own feed server, or null if not ours. */
  ownSeq: number | null
  /** The entry's thread feed on the aggregator, or "" if there is none. */
  threadUrl?: string
}>()
const emit = defineEmits<{ reply: []; delete: []; thread: [] }>()

const alternate = computed(() => linkHref(props.entry.links, 'alternate'))

// Where the entry came from, shown for unsigned entries instead of the
// author, whose identity is only a claim then.
const from = computed(() => {
  const src = linkHref(props.entry.links, 'via') || alternate.value
  try {
    return new URL(src).host
  } catch {
    return ''
  }
})

// amfeed tombstones have empty content and title (the API rejects empty posts).
const deleted = computed(() => !props.entry.content?.body && !props.entry.title && !props.entry.verb)

// HTML content comes from arbitrary feeds and is rendered in our origin, where
// the token lives, so it must be sanitized.
const html = computed(() =>
  props.entry.content?.type === 'html' ? DOMPurify.sanitize(props.entry.content.body) : '',
)

const published = computed(() => {
  const d = new Date(props.entry.published || props.entry.updated)
  return isNaN(d.getTime()) ? props.entry.published : d.toLocaleString()
})

// Long content is folded to FOLD_LINES. Only fold when that hides a fair
// amount; folding a six-line post to five just adds a click. The content's
// height changes after mount (images, fonts), so it is observed.
const FOLD_LINES = 5
const body = ref<HTMLElement>()
const foldable = ref(false)
const expanded = ref(false)
let observer: ResizeObserver | undefined

function measure() {
  const el = body.value
  if (!el) return
  const line = parseFloat(getComputedStyle(el).lineHeight) || 24
  foldable.value = el.offsetHeight > line * (FOLD_LINES + 2)
}

onMounted(() => {
  measure()
  if (body.value && typeof ResizeObserver !== 'undefined') {
    observer = new ResizeObserver(measure)
    observer.observe(body.value)
  }
})
onBeforeUnmount(() => observer?.disconnect())

const badge = computed(() => {
  switch (props.verified.state) {
    case 'pending':
      return { text: '…', title: 'verifying signature' }
    case 'ok':
      return { text: '✓', title: 'signature verified' }
    case 'unsigned':
      return { text: `unverified · from ${from.value}`, title: 'not signed; the author is only a claim' }
    case 'failed':
      return { text: '✗', title: `signature invalid: ${props.verified.reason}` }
    case 'unverifiable':
      return { text: '?', title: props.verified.reason }
  }
})
</script>

<template>
  <article :class="['entry', { deleted }]">
    <div class="meta">
      <span class="author">{{ entry.author?.name ?? 'unknown' }}</span>
      <span :class="['sig', verified.state]" :title="badge.title">{{ badge.text }}</span>
      <a v-if="alternate" :href="alternate" target="_blank" rel="noopener" class="time">{{ published }}</a>
      <span v-else class="time">{{ published }}</span>
    </div>
    <template v-if="deleted">
      <p class="muted">This post was deleted.</p>
    </template>
    <template v-else>
      <p v-if="entry.inReplyTo" class="muted in-reply-to">
        ↳ in reply to
        <a v-if="entry.inReplyTo.href" :href="entry.inReplyTo.href" target="_blank" rel="noopener">{{
          entry.inReplyTo.href
        }}</a>
        <span v-else>{{ entry.inReplyTo.ref }}</span>
      </p>
      <h2 v-if="entry.title">{{ entry.title }}</h2>
      <div :class="['fold', { folded: foldable && !expanded }]">
        <div ref="body">
          <div v-if="html" class="content" v-html="html" />
          <p v-else-if="entry.content" class="content text">{{ entry.content.body }}</p>
        </div>
      </div>
      <button v-if="foldable" class="link fold-toggle" @click="expanded = !expanded">
        {{ expanded ? '▴ less' : '▾ more' }}
      </button>
      <div class="actions">
        <button class="link" @click="emit('reply')">Reply</button>
        <button v-if="threadUrl" class="link" @click="emit('thread')">Thread</button>
        <button v-if="ownSeq !== null" class="link danger" @click="emit('delete')">Delete</button>
      </div>
    </template>
  </article>
</template>
