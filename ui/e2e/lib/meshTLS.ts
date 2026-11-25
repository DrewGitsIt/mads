import { createHash, createPrivateKey, createPublicKey, generateKeyPairSync, randomBytes, sign, X509Certificate, type KeyObject } from "node:crypto";
import { readFileSync } from "node:fs";

// Mesh TLS for the e2e stand-in Mesh Service (docs/MESH-AUTH-DESIGN.md D2):
// a device is an Ed25519 key, presented in a self-signed certificate, and
// identified by its fingerprint — the SHA-256 of the raw 32-byte public key
// in unpadded base32 (pkg/devicekey). Node can't mint certificates, so this
// builds the few DER structures a self-signed Ed25519 certificate needs.

const ED25519_OID = Buffer.from([0x06, 0x03, 0x2b, 0x65, 0x70]);
const CN_OID = Buffer.from([0x06, 0x03, 0x55, 0x04, 0x03]);

function der(tag: number, content: Buffer): Buffer {
  const n = content.length;
  const len = n < 0x80 ? Buffer.from([n]) : n < 0x100 ? Buffer.from([0x81, n]) : Buffer.from([0x82, n >> 8, n & 0xff]);
  return Buffer.concat([Buffer.from([tag]), len, content]);
}

const seq = (...parts: Buffer[]) => der(0x30, Buffer.concat(parts));

function utcTime(d: Date): Buffer {
  const p = (n: number) => String(n).padStart(2, "0");
  const s = `${p(d.getUTCFullYear() % 100)}${p(d.getUTCMonth() + 1)}${p(d.getUTCDate())}${p(d.getUTCHours())}${p(d.getUTCMinutes())}${p(d.getUTCSeconds())}Z`;
  return der(0x17, Buffer.from(s, "ascii"));
}

function base32(buf: Buffer): string {
  const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567";
  let bits = 0;
  let value = 0;
  let out = "";
  for (const byte of buf) {
    value = (value << 8) | byte;
    bits += 8;
    while (bits >= 5) {
      out += alphabet[(value >>> (bits - 5)) & 31];
      bits -= 5;
    }
  }
  if (bits > 0) out += alphabet[(value << (5 - bits)) & 31];
  return out;
}

/** The device fingerprint of an Ed25519 public key. */
export function fingerprintOf(pub: KeyObject): string {
  const spki = pub.export({ type: "spki", format: "der" });
  return base32(createHash("sha256").update(spki.subarray(spki.length - 32)).digest());
}

/** The fingerprint of the device key in a mads home ($MADS_HOME/device.key). */
export function deviceFingerprint(madsHome: string): string {
  return fingerprintOf(createPublicKey(createPrivateKey(readFileSync(`${madsHome}/device.key`))));
}

/** The device fingerprint a peer's certificate (DER) carries. */
export function certFingerprint(raw: Buffer): string {
  return fingerprintOf(new X509Certificate(raw).publicKey);
}

/** A fresh device: an Ed25519 key and a self-signed certificate for it. */
export function newDevice(name: string): { keyPem: string; certPem: string; fingerprint: string } {
  const { publicKey, privateKey } = generateKeyPairSync("ed25519");
  const subject = seq(der(0x31, seq(CN_OID, der(0x0c, Buffer.from(name, "utf8")))));
  const serial = randomBytes(16);
  // Positive (high bit clear) and minimal (no leading zero byte): DER
  // rejects a padded INTEGER ("illegal padding").
  serial[0] = (serial[0] & 0x7f) | 0x01;
  const now = Date.now();
  const tbs = seq(
    der(0xa0, der(0x02, Buffer.from([2]))), // v3
    der(0x02, serial),
    seq(ED25519_OID),
    subject,
    seq(utcTime(new Date(now - 3600_000)), utcTime(new Date(now + 365 * 24 * 3600_000))),
    subject,
    publicKey.export({ type: "spki", format: "der" }),
  );
  const signature = sign(null, tbs, privateKey);
  const cert = seq(tbs, seq(ED25519_OID), der(0x03, Buffer.concat([Buffer.from([0]), signature])));
  const certPem = `-----BEGIN CERTIFICATE-----\n${cert.toString("base64").replace(/.{64}/g, "$&\n")}\n-----END CERTIFICATE-----\n`;
  return {
    keyPem: privateKey.export({ type: "pkcs8", format: "pem" }).toString(),
    certPem,
    fingerprint: fingerprintOf(publicKey),
  };
}
