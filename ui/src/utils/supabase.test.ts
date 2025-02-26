import { testRequest, fetchConnections, addConnection } from './supabase';

const supabaseTestUUID: string = process.env.SUPABASE_TEST_UUID || '';

let connectionsQty: number = 0;

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

describe('fetchConnections', () => {
    test('should fetch connections successfully', async () => {
        try {
            console.log('Starting fetchConnections...');
            const result = await fetchConnections();
            console.log('Result of testRequest:', result);
            if (result) {
                connectionsQty = result.length;
                console.log('Number of connections:', connectionsQty);
            } else {
                console.log('No connections found');
            }
            console.log('fetchConnections completed successfully');
        } catch (error) {
            console.error('Error during fetchConnections execution');
            throw error;
        }
    });
});

describe('addConnection', () => {
    test('should add a connection successfully', async () => {
        try {
            console.log('Starting addConnection...');
            const result = await addConnection(supabaseTestUUID);
            if (!result) {
                console.log('No result returned from addConnection');
                throw new Error('No result returned from addConnection');
            }
        } catch (error) {
            console.error('Error during addConnection execution');
            throw error;
        }
    });
});