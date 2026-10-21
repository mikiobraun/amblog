<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { linkHref, type Entry } from '../lib/atom'
import { createEntry } from '../lib/api'

const props = defineProps<{ replyTo: Entry | null }>()
const emit = defineEmits<{ posted: []; cancelReply: [] }>()

const text = ref('')
const busy = ref(false)
const error = ref('')
const textarea = ref<HTMLTextAreaElement | null>(null)

const replyHref = computed(() => (props.replyTo ? linkHref(props.replyTo.links, 'alternate') : ''))

watch(
  () => props.replyTo,
  (r) => {
    if (r) textarea.value?.focus()
  },
)

async function submit() {
  if (!text.value.trim() || busy.value) return
  busy.value = true
  error.value = ''
  try {
    await createEntry({
      content: text.value,
      content_type: 'text',
      in_reply_to: props.replyTo ? { ref: props.replyTo.id, href: replyHref.value } : undefined,
    })
    text.value = ''
    emit('posted')
  } catch (e) {
    error.value = (e as Error).message
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <form class="compose" @submit.prevent="submit">
    <p v-if="replyTo" class="replying">
      ↳ Replying to <a :href="replyHref" target="_blank" rel="noopener">{{ replyTo.author?.name ?? replyTo.id }}</a>
      <button type="button" class="link" @click="emit('cancelReply')">cancel</button>
    </p>
    <textarea
      ref="textarea"
      v-model="text"
      rows="3"
      :placeholder="replyTo ? 'Write a reply…' : 'What\'s happening?'"
      @keydown.enter.ctrl.prevent="submit"
      @keydown.enter.meta.prevent="submit"
    />
    <div class="compose-footer">
      <span v-if="error" class="error">{{ error }}</span>
      <button type="submit" class="primary" :disabled="busy || !text.trim()">
        {{ replyTo ? 'Reply' : 'Post' }}
      </button>
    </div>
  </form>
</template>
