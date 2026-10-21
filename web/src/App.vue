<script setup lang="ts">
import AuthGate from './AuthGate.vue'
import AppShell from './AppShell.vue'
import { provideConsole } from './console'

const { base, error, login } = provideConsole()
</script>

<template>
  <AuthGate v-if="!base" :error="error" @ready="login" />
  <AppShell v-else />
</template>
