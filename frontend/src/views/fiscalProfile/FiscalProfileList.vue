<script setup lang="ts">
import ProfileCollectionListPage from "@/components/ProfileCollectionListPage.vue";
import { fiscalProfileCollection } from "@/collections/fiscalProfile.gen";
</script>

<template>
	<ProfileCollectionListPage :collection="fiscalProfileCollection" />
</template>
