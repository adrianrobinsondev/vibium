package com.vibium.internal;

import com.vibium.errors.VibiumConnectionException;

import java.util.concurrent.TimeUnit;

/**
 * Ensures Chrome for Testing is installed before launching a browser.
 *
 * Mirrors the Python client's {@code ensure_browser_installed()} behaviour:
 * runs {@code vibium paths} to locate Chrome, and if missing runs
 * {@code vibium install} to download it automatically.
 */
public final class BrowserInstaller {

    private BrowserInstaller() {}

    /**
     * Ensure Chrome for Testing is available on this machine.
     *
     * @param binaryPath path to the vibium binary
     * @throws VibiumConnectionException if installation fails
     */
    public static void ensureInstalled(String binaryPath) {
        // Respect VIBIUM_SKIP_BROWSER_DOWNLOAD=1
        String skip = System.getenv("VIBIUM_SKIP_BROWSER_DOWNLOAD");
        if ("1".equals(skip) || "true".equalsIgnoreCase(skip)) {
            return;
        }

        // Run "vibium paths" and check if Chrome binary exists
        if (isChromeInstalled(binaryPath)) {
            return;
        }

        // Chrome not found — download it
        System.out.println("Downloading Chrome for Testing...");
        System.out.flush();

        try {
            ProcessBuilder pb = new ProcessBuilder(binaryPath, "install");
            pb.inheritIO();
            Process process = pb.start();

            boolean finished = process.waitFor(5, TimeUnit.MINUTES);
            if (!finished) {
                process.destroyForcibly();
                throw new VibiumConnectionException("Chrome installation timed out");
            }

            int exitCode = process.exitValue();
            if (exitCode != 0) {
                throw new VibiumConnectionException(
                    "Failed to install Chrome (exit code " + exitCode + ")"
                );
            }

            System.out.println("Chrome installed successfully.");
            System.out.flush();
        } catch (VibiumConnectionException e) {
            throw e;
        } catch (Exception e) {
            throw new VibiumConnectionException("Failed to install Chrome: " + e.getMessage(), e);
        }
    }

    private static boolean isChromeInstalled(String binaryPath) {
        try {
            ProcessBuilder pb = new ProcessBuilder(binaryPath, "is-installed");
            pb.redirectErrorStream(true);
            Process process = pb.start();

            boolean finished = process.waitFor(10, TimeUnit.SECONDS);
            if (!finished) {
                process.destroyForcibly();
                return false;
            }

            return process.exitValue() == 0;
        } catch (Exception e) {
            return false;
        }
    }
}
