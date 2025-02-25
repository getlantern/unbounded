import { testRequest } from './supabase';

describe('testRequest', () => {
    test('should complete successfully', async () => {
        try {
            console.log('Starting testRequest...');
            await testRequest();
            console.log('testRequest completed successfully');
        } catch (error) {
            console.error('Error during testRequest execution');
            throw error; // Rethrow the error to fail the test
        }
    });
});
