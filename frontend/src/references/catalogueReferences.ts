import {
	defineCrudReference,
	type ReferenceListFn,
} from "@katren/vue-collection-lib";

import { fiscalProfileApi } from "@/api/fiscalProfile.gen";
import { paymentProfileApi } from "@/api/paymentProfile.gen";
import { salonApi } from "@/api/salon.gen";
import { yClientsProfileApi } from "@/api/yClientsProfile.gen";

type CatalogueKey = { id: number };
type CatalogueRow = {
	id: number;
	name: string;
	company?: Record<string, unknown> | null;
};

const catalogueReference = (
	list: ReferenceListFn<CatalogueRow>,
	detail: (key: CatalogueKey) => Promise<CatalogueRow>,
) =>
	defineCrudReference<CatalogueRow, CatalogueKey, number>({
		list,
		detail,
		keyField: "id",
		searchField: "name",
		format: (row) => {
			const companyName = row.company?.descr;
			return typeof companyName === "string" &&
				companyName !== ""
				? `${row.name} (${companyName})`
				: row.name;
		},
		fallback: (id) => `#${id}`,
		isEmpty: (id) => id <= 0,
		emptyValue: null,
	});

// Profiles can only be assigned to companies with a configured salon.
export const companyReference = catalogueReference(
	async (params) => {
		const { rows, agg } = await salonApi.list(params);
		return {
			rows: rows.map(({ company_id, name }) => ({
				id: company_id,
				name,
			})),
			agg,
		};
	},
	async ({ id }) => {
		const { name } = await salonApi.detail({ company_id: id });
		return { id, name };
	},
);
export const fiscalProfileReference = catalogueReference(
	fiscalProfileApi.list,
	fiscalProfileApi.detail,
);
export const paymentProfileReference = catalogueReference(
	paymentProfileApi.list,
	paymentProfileApi.detail,
);
export const yclientsProfileReference = catalogueReference(
	yClientsProfileApi.list,
	yClientsProfileApi.detail,
);
