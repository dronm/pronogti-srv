import {
	FilterOperatorParam,
	SortDirect,
	type CollectionParams,
	type CollectionResponse,
} from "@katren/vue-collection-lib";
import type { RouteLocationRaw } from "vue-router";

import { companyApi } from "@/api/company.gen";
import { fiscalProfileApi } from "@/api/fiscalProfile.gen";
import { paymentProfileApi } from "@/api/paymentProfile.gen";
import { salonApi } from "@/api/salon.gen";
import { yClientsProfileApi } from "@/api/yClientsProfile.gen";

export type CatalogueSelectOption = {
	value: number;
	label: string;
};

export type CatalogueSelectReference = {
	loadOptions: () => Promise<CatalogueSelectOption[]>;
	loadOption: (id: number) => Promise<CatalogueSelectOption | null>;
	collectionRoute: RouteLocationRaw | null;
	openRoute?: (id: number) => RouteLocationRaw;
};

type CatalogueRow = {
	id: number;
	name: string;
};

type ProfileRow = CatalogueRow & { company_id: number };
type CatalogueList<TRow> = (
	params?: CollectionParams,
) => Promise<CollectionResponse<TRow>>;
type ProfileCatalogue = {
	list: CatalogueList<ProfileRow>;
	detail: (key: { id: number }) => Promise<ProfileRow>;
};

const pageSize = 100;

const loadCatalogueRows = async <TRow extends { name: string }>(
	list: CatalogueList<TRow>,
	filter?: CollectionParams["filter"],
	keyField = "id",
): Promise<TRow[]> => {
	const rows: TRow[] = [];
	let total = 0;

	do {
		const page = await list({
			from: rows.length,
			count: pageSize,
			filter,
			sorter: [
				{ f: "name", d: SortDirect.ASC },
				{ f: keyField, d: SortDirect.ASC },
			],
		});
		rows.push(...page.rows);
		total = page.agg.tot_count;
		if (page.rows.length === 0) {
			break;
		}
	} while (rows.length < total);

	return rows;
};

const toOption = (row: CatalogueRow): CatalogueSelectOption => ({
	value: row.id,
	label: row.name,
});

export const companySelectReference: CatalogueSelectReference = {
	loadOptions: async () =>
		(await loadCatalogueRows<CatalogueRow>(companyApi.list)).map(toOption),
	loadOption: async (id) => toOption(await companyApi.detail({ id })),
	collectionRoute: { name: "companies" },
	openRoute: (id) => ({ name: "companyEdit", params: { id } }),
};

export const salonCompanySelectReference: CatalogueSelectReference = {
	loadOptions: async () =>
		(await loadCatalogueRows<{ company_id: number; name: string }>(salonApi.list, undefined, "company_id")).map(({ company_id, name }) => ({
			value: company_id,
			label: name,
		})),
	loadOption: async (id) => ({ value: id, label: (await salonApi.detail({ company_id: id })).name }),
	collectionRoute: { name: "salons" },
	openRoute: (id) => ({ name: "salonEdit", params: { company_id: id } }),
};

const createProfileSelectReference = (
	api: ProfileCatalogue,
	routeName: string,
	companyID: number | null,
	editRouteName?: string,
): CatalogueSelectReference => ({
	loadOptions: async () => {
		if (companyID === null) {
			return [];
		}

		const rows = await loadCatalogueRows(api.list, [
			{
				f: {
					company_id: {
						o: FilterOperatorParam.E,
						v: companyID,
					},
				},
			},
		]);
		return rows
			.filter((row) => row.company_id === companyID)
			.map(toOption);
	},
	loadOption: async (id) => {
		if (companyID === null) {
			return null;
		}

		const row = await api.detail({ id });
		return row.company_id === companyID ? toOption(row) : null;
	},
	collectionRoute: companyID === null ? null : { name: routeName },
	openRoute: (id) => editRouteName
		? { name: editRouteName, params: { id: String(id) } }
		: { name: routeName, query: { id: String(id) } },
});

export const createSalonProfileSelectReferences = (
	companyID: number | null,
) => ({
	payment: createProfileSelectReference(
		paymentProfileApi,
		"paymentProfiles",
		companyID,
		"paymentProfileEdit",
	),
	fiscal: createProfileSelectReference(
		fiscalProfileApi,
		"fiscalProfiles",
		companyID,
		"fiscalProfileEdit",
	),
	yclients: createProfileSelectReference(
		yClientsProfileApi,
		"yclientsProfiles",
		companyID,
	),
});
