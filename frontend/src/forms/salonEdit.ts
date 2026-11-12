import type { SalonFormModel } from "@/forms/salon.gen";
import type { SalonDetail } from "@/types/salonDetail.gen";

export type SalonEditModel = SalonFormModel & Partial<Pick<
	SalonDetail,
	| "company"
	| "default_payment_profile"
	| "default_fiscal_profile"
	| "default_yclients_profile"
>>;
