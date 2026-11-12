type FiscalCodeOption = {
	value: number;
	label: string;
};

// Numeric TerminalFA receipt protocol values for FFD 1.2 tags 1199, 1055 and 1214.
export const fiscalVatOptions: FiscalCodeOption[] = [
	{ value: 1, label: "НДС 20%" },
	{ value: 2, label: "НДС 10%" },
	{ value: 3, label: "НДС 20/120" },
	{ value: 4, label: "НДС 10/110" },
	{ value: 5, label: "НДС 0%" },
	{ value: 6, label: "Без НДС" },
	{ value: 7, label: "НДС 5%" },
	{ value: 8, label: "НДС 7%" },
	{ value: 9, label: "НДС 5/105" },
	{ value: 10, label: "НДС 7/107" },
	{ value: 11, label: "НДС 22%" },
	{ value: 12, label: "НДС 22/122" },
];

export const fiscalTaxSystemOptions: FiscalCodeOption[] = [
	{ value: 1, label: "ОСНО" },
	{ value: 2, label: "УСН «Доходы»" },
	{ value: 4, label: "УСН «Доходы минус расходы»" },
	{ value: 16, label: "ЕСХН" },
	{ value: 32, label: "ПСН" },
];

export const fiscalPaymentMethodOptions: FiscalCodeOption[] = [
	{ value: 1, label: "Предоплата 100%" },
	{ value: 2, label: "Предоплата" },
	{ value: 3, label: "Аванс" },
	{ value: 4, label: "Полный расчёт" },
	{ value: 5, label: "Частичный расчёт и кредит" },
	{ value: 6, label: "Передача в кредит" },
	{ value: 7, label: "Оплата кредита" },
];
