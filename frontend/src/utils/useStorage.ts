import { useState } from 'react';

function readStorage(key: string) {
  try {
    return window.localStorage.getItem(key) || undefined;
  } catch {
    return undefined;
  }
}

function useStorage(
  key: string,
  defaultValue?: string
): [string, (value: string) => void] {
  const [storedValue, setStoredValue] = useState(
    () => readStorage(key) || defaultValue
  );

  const setStorageValue = (value: string) => {
    try {
      window.localStorage.setItem(key, value);
    } catch {
      // 存储不可用时仍允许当前页面切换语言。
    }
    setStoredValue(value);
  };

  return [storedValue, setStorageValue];
}

export default useStorage;
