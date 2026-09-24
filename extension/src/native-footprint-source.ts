import JSZip from 'jszip';

import { projectFootprintSourceInventory } from './util';

const MAX_ARCHIVE_BYTES = 64 * 1024 * 1024;
const MAX_SOURCE_BYTES = 64 * 1024 * 1024;
const CRC32_TABLE = Array.from({ length: 256 }, (_, value) => {
	for (let bit = 0; bit < 8; bit++) value = value & 1 ? 0xedb88320 ^ (value >>> 1) : value >>> 1;
	return value >>> 0;
});

/** Decode the single root .epru in an official epro2 export, without extracting
 * images or writing any archive content to disk. Bound decompression as it runs.
 */
export async function readProjectNativeSourceArchive(file: Blob): Promise<string> {
	if (!file || file.size <= 0 || file.size > MAX_ARCHIVE_BYTES) throw new Error('official project archive is empty or exceeds 64 MiB');
	const bytes = await file.arrayBuffer();
	if (bytes.byteLength !== file.size || bytes.byteLength > MAX_ARCHIVE_BYTES) throw new Error('official project archive size changed while reading');
	const zip = await JSZip.loadAsync(bytes);
	const files = Object.values(zip.files);
	if (files.length > 2048) throw new Error('official project archive exceeds 2048 entries');
	const sources = files.filter(f => !f.dir && /^[^/\\]+\.epru$/i.test(f.name));
	if (sources.length !== 1) throw new Error(`official project archive has ${sources.length} root .epru files; one is required`);
	const source = sources[0];
	const originalName = (source as typeof source & { unsafeOriginalName?: string }).unsafeOriginalName;
	if (originalName && originalName !== source.name) throw new Error('native source archive path was rewritten');
	// JSZip does not expose per-entry integrity validation publicly: its global
	// checkCRC32 inflates every archive entry, bypassing our source-only budget.
	// Guard its parsed entry metadata, then verify this one stream's length/CRC.
	const metadata = (source as typeof source & { _data?: { uncompressedSize?: number; crc32?: number } })._data;
	if (!metadata || !Number.isSafeInteger(metadata.uncompressedSize) || !Number.isInteger(metadata.crc32)
		|| metadata.uncompressedSize! < 0 || metadata.uncompressedSize! > MAX_SOURCE_BYTES) throw new Error('native source ZIP integrity metadata is unavailable or exceeds 64 MiB');
	// JSZip 3.10 implements/documents ZipObject.internalStream, but its bundled
	// declarations omit it. The narrow bridge retains a runtime capability check.
	const streamingSource = source as typeof source & {
		internalStream(type: 'uint8array'): JSZip.JSZipStreamHelper<Uint8Array>;
	};
	if (typeof streamingSource.internalStream !== 'function') throw new Error('bounded ZIP source streaming is unavailable');
	const stream = streamingSource.internalStream('uint8array');
	const chunks: Array<Uint8Array> = [];
	let length = 0;
	let crc = 0xffffffff;
	const sourceBytes = await new Promise<Uint8Array>((resolve, reject) => {
		let failed = false;
		stream.on('data', (chunk: Uint8Array) => {
			if (failed) return;
			length += chunk.byteLength;
			if (length > MAX_SOURCE_BYTES) {
				failed = true;
				stream.pause();
				reject(new Error('native source exceeds 64 MiB'));
				return;
			}
			for (const byte of chunk) crc = CRC32_TABLE[(crc ^ byte) & 0xff] ^ (crc >>> 8);
			chunks.push(chunk);
		});
		stream.on('error', reject);
		stream.on('end', () => {
			if (failed) return;
			if (length !== metadata.uncompressedSize || ((crc ^ 0xffffffff) >>> 0) !== (metadata.crc32! >>> 0)) {
				reject(new Error('native source ZIP length/CRC32 verification failed'));
				return;
			}
			const result = new Uint8Array(length);
			let offset = 0;
			for (const chunk of chunks) { result.set(chunk, offset); offset += chunk.byteLength; }
			resolve(result);
		});
		stream.resume();
	});
	return new TextDecoder('utf-8', { fatal: true }).decode(sourceBytes);
}

export async function readProjectFootprintSourceArchive(
	file: Blob,
	documentUuid: string,
): Promise<Array<{ footprintUuid: string; documentSource: string }>> {
	return projectFootprintSourceInventory(await readProjectNativeSourceArchive(file), documentUuid);
}
