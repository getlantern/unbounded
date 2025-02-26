import { signInAnon, fetchConnections, addConnection } from './supabase';

const supabaseTestUUID: string = process.env.SUPABASE_TEST_UUID || '';

let connectionsQty: number = 0;

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
            if (!result.success) {
                throw new Error(`Failed to add connection: ${result.error}`);
            } else {
                console.log('Connection added successfully:', result.data);
                // Verify the connection was added
                const connections = await fetchConnections();
                if (connections) {
                    expect(connections.length).toBe(connectionsQty + 1);
                    console.log('Connection count verified successfully');
                } else {
                    throw new Error('Failed to fetch connections after adding');
                }
            }
        } catch (error) {
            console.error('Error during addConnection execution');
            throw error;
        }
    });
});

describe('sign in anon', () => {
    test('should sign in anonymously', async () => {
        try {
            console.log('Starting signInAnon...');
            const result = await signInAnon();
            if (result.success) {
                console.log('Anonymous sign-in successful:', result.data);
            } else {
                throw new Error(`Anonymous sign-in failed: ${result.error}`);
            }
        } catch (error) {
            console.error('Error during signInAnon execution');
            throw error;
        }
    })
});
