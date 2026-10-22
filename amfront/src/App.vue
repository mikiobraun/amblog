<script setup lang="ts">
import { ref } from 'vue'
import { isConfigured } from './lib/settings'
import TimelineView from './components/TimelineView.vue'
import FollowingView from './components/FollowingView.vue'
import SettingsView from './components/SettingsView.vue'

type View = 'timeline' | 'following' | 'settings'
const view = ref<View>(isConfigured() ? 'timeline' : 'settings')
</script>

<template>
  <header>
    <h1><span class="logo" aria-hidden="true">⚛</span> amfront</h1>
    <nav>
      <button :class="{ active: view === 'timeline' }" @click="view = 'timeline'">Timeline</button>
      <button :class="{ active: view === 'following' }" @click="view = 'following'">Following</button>
      <button :class="{ active: view === 'settings' }" @click="view = 'settings'">Settings</button>
    </nav>
  </header>
  <main>
    <TimelineView v-if="view === 'timeline'" />
    <FollowingView v-else-if="view === 'following'" />
    <SettingsView v-else @saved="view = 'timeline'" />
  </main>
</template>
