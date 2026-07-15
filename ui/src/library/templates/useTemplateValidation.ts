import { useCallback, useState } from "react";
import { router, RouterValidationError } from "../../router/client";

export interface FieldError {
  field: string;
  message: string;
}

export interface UseTemplateValidation {
  errors: FieldError[];
  validating: boolean;
  valid: boolean | null;
  validate: (source: string) => Promise<boolean>;
}

/**
 * POST /templates/validate — pkg/template's shared Check reports every
 * failure together, so one call surfaces the full FieldError[] rather than
 * one-at-a-time round trips.
 */
export function useTemplateValidation(): UseTemplateValidation {
  const [errors, setErrors] = useState<FieldError[]>([]);
  const [validating, setValidating] = useState(false);
  const [valid, setValid] = useState<boolean | null>(null);

  const validate = useCallback(async (source: string) => {
    setValidating(true);
    try {
      await router.validateTemplate(source);
      setErrors([]);
      setValid(true);
      return true;
    } catch (err) {
      if (err instanceof RouterValidationError) {
        setErrors(err.errors);
      } else {
        setErrors([{ field: "source", message: err instanceof Error ? err.message : String(err) }]);
      }
      setValid(false);
      return false;
    } finally {
      setValidating(false);
    }
  }, []);

  return { errors, validating, valid, validate };
}
