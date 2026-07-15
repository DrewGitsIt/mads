export interface FieldError {
  field: string;
  message: string;
}

/** ValidationError.errors[] -> Record<field, message>, for inline display next to each input. */
export function fieldErrorMap(errors: FieldError[]): Record<string, string> {
  const map: Record<string, string> = {};
  for (const e of errors) map[e.field] = e.message;
  return map;
}
