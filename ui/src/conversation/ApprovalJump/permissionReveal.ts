/**
 * A request to bring one permission card into the DOM: the approval jump
 * publishes it (a fresh nonce per click, so re-revealing the same id still
 * fires), and a collapsed ActivityGroup holding that pending card opens.
 */
export interface PermissionReveal {
  id: string;
  nonce: number;
}
