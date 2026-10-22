<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { linkHref, type Entry } from '../lib/atom'
import { createEntry } from '../lib/api'

const props = defineProps<{ replyTo: Entry | null }>()
const emit = defineEmits<{ posted: []; cancelReply: [] }>()

const text = ref('')
const busy = ref(false)
const error = ref('')
const textarea = ref<HTMLTextAreaElement | null>(null)

const replyHref = computed(() => (props.replyTo ? linkHref(props.replyTo.links, 'alternate') : ''))

// A reply box opens under the post it answers, ready to type.
onMounted(() => {
  if (props.replyTo) textarea.value?.focus()
})

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
  <form :class="['compose', { reply: replyTo }]" @submit.prevent="submit">
    <p v-if="replyTo" class="replying">
      ↳ Replying to <a :href="replyHref" target="_blank" rel="noopener">{{ replyTo.author?.name ?? replyTo.id }}</a>
    </p>
    <textarea
      ref="textarea"
      v-model="text"
      rows="3"
      :placeholder="replyTo ? 'Write a reply…' : 'What\'s happening?'"
      @keydown.enter.ctrl.prevent="submit"
      @keydown.enter.meta.prevent="submit"
      @keydown.esc="replyTo && emit('cancelReply')"
    />
    <div class="compose-footer">
      <span v-if="error" class="error">{{ error }}</span>
      <button v-if="replyTo" type="button" class="secondary" @click="emit('cancelReply')">Cancel</button>
      <button type="submit" class="primary" :disabled="busy || !text.trim()">
        {{ replyTo ? 'Reply' : 'Post' }}
      </button>
    </div>
  </form>
</template>
