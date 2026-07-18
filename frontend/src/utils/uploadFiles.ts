interface StoredUploadFile {
  key: string;
  ownerId: number;
  sessionId: string;
  checksum: string;
  file: File;
}

const DB_NAME = 'xiaoyuposthub-uploads';
const STORE_NAME = 'files';

function openDatabase(): Promise<IDBDatabase> {
  return new Promise((resolve, reject) => {
    const request = indexedDB.open(DB_NAME, 1);
    request.onupgradeneeded = () => {
      const database = request.result;
      if (!database.objectStoreNames.contains(STORE_NAME)) {
        const store = database.createObjectStore(STORE_NAME, {
          keyPath: 'key',
        });
        store.createIndex('ownerId', 'ownerId', { unique: false });
      }
    };
    request.onsuccess = () => resolve(request.result);
    request.onerror = () => reject(request.error);
  });
}

function taskKey(ownerId: number, sessionId: string) {
  return `${ownerId}:${sessionId}`;
}

export async function saveUploadFile(
  ownerId: number,
  sessionId: string,
  checksum: string,
  file: File
) {
  const database = await openDatabase();
  await new Promise<void>((resolve, reject) => {
    const transaction = database.transaction(STORE_NAME, 'readwrite');
    transaction.objectStore(STORE_NAME).put({
      key: taskKey(ownerId, sessionId),
      ownerId,
      sessionId,
      checksum,
      file,
    } as StoredUploadFile);
    transaction.oncomplete = () => resolve();
    transaction.onerror = () => reject(transaction.error);
  });
  database.close();
}

export async function loadUploadFile(ownerId: number, sessionId: string) {
  const database = await openDatabase();
  const result = await new Promise<StoredUploadFile | undefined>(
    (resolve, reject) => {
      const request = database
        .transaction(STORE_NAME, 'readonly')
        .objectStore(STORE_NAME)
        .get(taskKey(ownerId, sessionId));
      request.onsuccess = () => resolve(request.result);
      request.onerror = () => reject(request.error);
    }
  );
  database.close();
  return result;
}

export async function removeUploadFile(ownerId: number, sessionId: string) {
  const database = await openDatabase();
  await new Promise<void>((resolve, reject) => {
    const transaction = database.transaction(STORE_NAME, 'readwrite');
    transaction.objectStore(STORE_NAME).delete(taskKey(ownerId, sessionId));
    transaction.oncomplete = () => resolve();
    transaction.onerror = () => reject(transaction.error);
  });
  database.close();
}
