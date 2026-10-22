<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { follow, following, unfollow, type FollowedFeed } from '../lib/agg'
import { isLoggedIn } from '../lib/settings'

const feeds = ref<FollowedFeed[]>([])
const url = ref('')
const busy = ref(false)
const error = ref('')

async function load() {
  if (!isLoggedIn()) return
  try {
    feeds.value = await following()
  } catch (e) {
    error.value = (e as Error).message
  }
}

async function add() {
  busy.value = true
  error.value = ''
  try {
    await follow(url.value.trim())
    url.value = ''
    load()
  } catch (e) {
    error.value = (e as Error).message
  } finally {
    busy.value = false
  }
}

async function remove(f: FollowedFeed) {
  if (!confirm(`Unfollow ${f.title || f.feed_url}?`)) return
  try {
    await unfollow(f.feed_url)
    load()
  } catch (e) {
    error.value = (e as Error).message
  }
}

onMounted(load)
</script>

<template>
  <section class="settings">
    <h2>Following</h2>
    <p v-if="!isLoggedIn()" class="muted">Log in to an aggregator in Settings to follow feeds.</p>
    <template v-else>
      <form class="inline" @submit.prevent="add">
        <input v-model="url" type="url" placeholder="A feed, or a page linking one" required />
        <button type="submit" :disabled="busy">{{ busy ? 'Following…' : 'Follow' }}</button>
      </form>
      <p class="note">Any Atom feed: people on other aggregators, blogs, release notes. Followed feeds show up in your home timeline only.</p>
      <p v-if="error" class="error">{{ error }}</p>
      <ul class="plain">
        <li v-for="f in feeds" :key="f.feed_url">
          <a :href="f.html_url || f.feed_url" target="_blank" rel="noopener">{{ f.title || f.feed_url }}</a>
          <button class="link danger" @click="remove(f)">Unfollow</button>
        </li>
      </ul>
      <p v-if="feeds.length === 0" class="muted">You don't follow any feeds yet.</p>
    </template>
  </section>
</template>
