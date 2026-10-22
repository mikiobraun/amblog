<script setup lang="ts">
import { go, route } from './lib/route'
import TimelineView from './components/TimelineView.vue'
import FollowingView from './components/FollowingView.vue'
import SettingsView from './components/SettingsView.vue'
</script>

<template>
  <header>
    <h1><span class="logo" aria-hidden="true">⚛</span> amfront</h1>
    <nav>
      <button :class="{ active: route.view === 'timeline' }" @click="go('timeline')">Timeline</button>
      <button :class="{ active: route.view === 'following' }" @click="go('following')">Following</button>
      <button :class="{ active: route.view === 'settings' }" @click="go('settings')">Settings</button>
    </nav>
  </header>
  <main>
    <TimelineView v-if="route.view === 'timeline'" />
    <FollowingView v-else-if="route.view === 'following'" />
    <SettingsView v-else @saved="go('timeline')" />
  </main>
</template>
