// When the user last saved a backup file, so Settings can say whether this
// device's key and tokens are safe. Only a timestamp, never the file.
const KEY = 'arfl.lastBackup'

export function markBackedUp() {
  try {
    localStorage.setItem(KEY, String(Date.now()))
  } catch {
    // Without storage the status simply reads as not backed up.
  }
}

export function lastBackup(): number | null {
  try {
    const v = Number(localStorage.getItem(KEY))
    return v > 0 ? v : null
  } catch {
    return null
  }
}

export function backupAge(at: number): string {
  const mins = Math.floor((Date.now() - at) / 60000)
  if (mins < 2) return 'just now'
  if (mins < 60) return `${mins} min ago`
  const hours = Math.floor(mins / 60)
  if (hours < 48) return `${hours} h ago`
  return new Date(at).toLocaleDateString()
}

// Matches wallet.MinBackupPassphrase. The file holds the key and every token,
// and whoever copies it can guess offline, so short passphrases are refused.
export const MIN_BACKUP_PASSPHRASE = 12
