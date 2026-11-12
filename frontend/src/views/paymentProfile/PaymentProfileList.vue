<script setup lang="ts">
import ProfileCollectionListPage from "@/components/ProfileCollectionListPage.vue";
import { paymentProfileCollection } from "@/collections/paymentProfile.gen";

const collection = {
	...paymentProfileCollection,
	commands: paymentProfileCollection.commands?.filter(({ name }) => name !== "copy"),
};
</script>

<template>
	<ProfileCollectionListPage :collection="collection" />
</template>
