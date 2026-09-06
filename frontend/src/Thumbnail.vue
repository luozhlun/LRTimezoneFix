<script setup>
import { onMounted, onBeforeUnmount, ref } from 'vue';
import { thumbnail } from './thumbnails.js';
const props = defineProps({ file: Object, session: String });
const host = ref(null);
const src = ref('');
let observer;
let active = true;
onMounted(() => {
  if (props.file.fileType === 'XMP') return;
  observer = new IntersectionObserver(([entry]) => {
    if (!entry.isIntersecting) return;
    observer.disconnect();
    thumbnail(props.session, props.file.index).then(value => { if (active) src.value = value; });
  });
  observer.observe(host.value);
});
onBeforeUnmount(() => { active = false; observer?.disconnect(); });
</script>

<template>
  <span ref="host" class="thumbnail" :class="{ sidecar: file.fileType === 'XMP' }">
    <img v-if="src" :src="src" alt="照片预览">
    <span v-else>{{ file.fileType === 'XMP' ? 'XMP' : 'JPG' }}</span>
  </span>
</template>
