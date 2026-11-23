BEGIN;

-- A schema rollback must not erase issued, uncertain or pending fiscal history.
DO $$
BEGIN
	IF EXISTS (SELECT 1 FROM public.fiscal_corrections) OR EXISTS (SELECT 1 FROM public.fiscal_correction_documents) THEN
		RAISE EXCEPTION 'cannot remove fiscal correction history; preserve and reconcile the journal before rollback';
	END IF;
END;
$$;

DROP TABLE public.fiscal_correction_documents;
DROP TABLE public.fiscal_corrections;
DROP FUNCTION public.fiscal_correction_documents_immutable();
DROP FUNCTION public.fiscal_corrections_immutable();
COMMIT;
