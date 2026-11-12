<script setup lang="ts">
import ProfileCollectionListPage from "@/components/ProfileCollectionListPage.vue";
import { yClientsProfileCollection } from "@/collections/yClientsProfile.gen";
</script>

<template>
	<ProfileCollectionListPage :collection="yClientsProfileCollection" />
</template>
