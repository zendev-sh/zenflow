<script setup lang="ts">
import { ref, onMounted } from 'vue'

const props = defineProps<{
  id: string
  ariaLabel?: string
}>()

const host = ref<HTMLDivElement | null>(null)

onMounted(() => {
  if (!host.value) return
  const script = document.createElement('script')
  script.src = `https://asciinema.org/a/${props.id}.js`
  script.id = `asciicast-${props.id}`
  script.async = true
  host.value.appendChild(script)
})
</script>

<template>
  <div ref="host" class="zf-asciinema" role="img" :aria-label="ariaLabel"></div>
</template>

<style scoped>
.zf-asciinema {
  margin: 2.5rem auto;
  max-width: 920px;
}
.zf-asciinema :deep(iframe) {
  display: block;
  width: 100%;
}
</style>
